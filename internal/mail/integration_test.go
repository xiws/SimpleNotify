package mail

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/state"
)

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}

type literalReader struct{ *bytes.Reader }

func (r literalReader) Size() int64 { return int64(r.Len()) }

func startMemIMAP(t *testing.T) (string, *imapmemserver.User) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("user", "pass")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
		Logger:       discardLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), user
}

func memConfig(t *testing.T, addr string) config.Mail {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg := testMail()
	cfg.Host = host
	cfg.Port = port
	cfg.Security = "none"
	cfg.Username = "user"
	cfg.Password = "pass"
	cfg.OnStart = "all"
	cfg.PollInterval = 5e9
	cfg.MarkAsSeen = false
	return cfg
}

func TestCheckAgainstMemoryServer(t *testing.T) {
	addr, user := startMemIMAP(t)
	raw := rawMail("hello", "world")
	if _, err := user.Append("INBOX", literalReader{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append: %v", err)
	}

	cfg := memConfig(t, addr)
	info, err := Check(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info.Server != addr || info.Mailbox != "INBOX" {
		t.Fatalf("info = %+v", info)
	}
	if info.Messages != 1 || info.UIDNext != 2 || info.UIDValidity == 0 {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Capabilities) == 0 {
		t.Fatal("expected capabilities")
	}
}

func TestPollerAgainstMemoryServer(t *testing.T) {
	addr, user := startMemIMAP(t)
	raw := rawMail("deploy now", "please deploy")
	if _, err := user.Append("INBOX", literalReader{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append: %v", err)
	}

	cfg := memConfig(t, addr)
	cfg.OnStart = "all"

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	proc := &fakeProcessor{result: ProcessResult{Matched: true, Status: "success"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(NewDialer("test", logger), st, proc, func() config.Mail { return cfg }, logger)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(proc.got) != 1 {
		t.Fatalf("processed %d messages", len(proc.got))
	}
	got := proc.got[0]
	if got.Subject != "deploy now" || got.Body != "please deploy" || got.UID != 1 || got.Mailbox != "INBOX" {
		t.Fatalf("message = %+v", got)
	}
	if got.From != "<a@example.com>" {
		t.Fatalf("from = %q", got.From)
	}

	m, ok := st.Get("INBOX")
	if !ok || m.LastUID != 1 {
		t.Fatalf("state = %+v ok=%v", m, ok)
	}

	// A second poll must not re-process the same message.
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("second PollOnce: %v", err)
	}
	if len(proc.got) != 1 {
		t.Fatalf("reprocessed messages: %d", len(proc.got))
	}
}
