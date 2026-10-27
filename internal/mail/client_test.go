package mail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xiws/SimpleNotify/internal/config"
)

// scriptedIMAP is a minimal IMAP server used to emulate servers whose SELECT
// response omits UIDNEXT, like 163.com's Coremail.
type scriptedIMAP struct {
	uidValidity  uint32
	messages     uint32
	firstUID     uint32 // UID of message sequence number 1
	omitsUIDNext bool

	mu      sync.Mutex
	fetches int // number of plain FETCH commands (used to resolve UIDNEXT)
}

func (s *scriptedIMAP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	send := func(lines ...string) {
		for _, line := range lines {
			fmt.Fprintf(w, "%s\r\n", line)
		}
		if err := w.Flush(); err != nil {
			return
		}
	}

	send("* OK [CAPABILITY IMAP4rev1 ID] scripted server ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(strings.TrimRight(line, "\r\n"))
		if len(fields) < 2 {
			continue
		}
		tag, name := fields[0], strings.ToUpper(fields[1])

		switch name {
		case "CAPABILITY":
			send("* CAPABILITY IMAP4rev1 ID", tag+" OK CAPABILITY completed")
		case "LOGIN":
			send(tag + " OK LOGIN completed")
		case "ID":
			send(`* ID ("name" "scripted")`, tag+" OK ID completed")
		case "SELECT", "EXAMINE":
			lines := []string{
				fmt.Sprintf("* %d EXISTS", s.messages),
				"* 0 RECENT",
				"* FLAGS (\\Answered \\Seen)",
				fmt.Sprintf("* OK [UIDVALIDITY %d] UIDs valid", s.uidValidity),
			}
			if !s.omitsUIDNext {
				lines = append(lines,
					fmt.Sprintf("* OK [UIDNEXT %d] Predicted next UID", s.firstUID+s.messages))
			}
			lines = append(lines, tag+" OK [READ-WRITE] SELECT completed")
			send(lines...)
		case "FETCH":
			s.mu.Lock()
			s.fetches++
			s.mu.Unlock()
			seq := s.messages
			if len(fields) >= 3 {
				if start, _, ok := strings.Cut(fields[2], ":"); ok {
					if n, err := strconv.ParseUint(start, 10, 32); err == nil && n > 0 {
						seq = uint32(n)
					}
				}
			}
			uid := s.firstUID + seq - 1
			send(fmt.Sprintf("* %d FETCH (UID %d)", seq, uid), tag+" OK FETCH completed")
		case "SEARCH":
			send("* SEARCH", tag+" OK SEARCH completed")
		case "UID":
			sub := ""
			if len(fields) >= 3 {
				sub = strings.ToUpper(fields[2])
			}
			if sub == "SEARCH" {
				send("* SEARCH", tag+" OK SEARCH completed")
			} else {
				send(tag + " OK UID completed")
			}
		case "LOGOUT":
			send("* BYE logging out", tag+" OK LOGOUT completed")
			return
		default:
			send(tag + " OK " + name + " completed")
		}
	}
}

func startScriptedIMAP(t *testing.T, server *scriptedIMAP) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	return ln.Addr().String()
}

func scriptedConfig(t *testing.T, addr string) config.Mail {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testMail()
	cfg.Host = host
	cfg.Port = port
	cfg.Security = "none"
	cfg.Username = "u@example.com"
	cfg.Password = "p"
	return cfg
}

// TestUIDNextWithoutSelectHint covers servers that violate the RFC and omit
// UIDNEXT from SELECT (163.com does this): the next UID must be derived from
// the highest UID in the mailbox instead of failing the poll.
func TestUIDNextWithoutSelectHint(t *testing.T) {
	for _, omit := range []bool{true, false} {
		t.Run(fmt.Sprintf("omits_uidnext=%v", omit), func(t *testing.T) {
			server := &scriptedIMAP{uidValidity: 42, messages: 2, firstUID: 101, omitsUIDNext: omit}
			addr := startScriptedIMAP(t, server)
			cfg := scriptedConfig(t, addr)

			info, err := Check(context.Background(), cfg, "test")
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if info.UIDValidity != 42 || info.Messages != 2 || info.UIDNext != 103 {
				t.Fatalf("info = %+v, want UIDVALIDITY=42 messages=2 UIDNEXT=103", info)
			}

			conn, err := NewDialer("test", slog.New(slog.NewTextHandler(io.Discard, nil))).Dial(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			if got := conn.UIDNext(); got != 103 {
				t.Fatalf("UIDNext = %d, want 103", got)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			server.mu.Lock()
			fetches := server.fetches
			server.mu.Unlock()
			if omit && fetches != 2 {
				t.Fatalf("fallback FETCH count = %d, want 2 (one per connection)", fetches)
			}
			if !omit && fetches != 0 {
				t.Fatalf("fallback FETCH count = %d, want 0 when UIDNEXT is reported", fetches)
			}
		})
	}
}

func TestUIDNextEmptyMailboxWithoutSelectHint(t *testing.T) {
	server := &scriptedIMAP{uidValidity: 7, messages: 0, firstUID: 1, omitsUIDNext: true}
	addr := startScriptedIMAP(t, server)
	cfg := scriptedConfig(t, addr)

	info, err := Check(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info.Messages != 0 || info.UIDNext != 1 {
		t.Fatalf("info = %+v, want messages=0 UIDNEXT=1", info)
	}
	server.mu.Lock()
	fetches := server.fetches
	server.mu.Unlock()
	if fetches != 0 {
		t.Fatalf("fallback FETCH count = %d, want 0 for an empty mailbox", fetches)
	}
}
