package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/mail"
)

func writeConf(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "conf.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

const baseConf = `{
  "server": {"addr": "127.0.0.1:8899"},
  "log": {"level": "error"},
  "mail": {"enabled": false},
  "execute": {"shell": "/bin/bash", "timeout": "5s", "work_dir": "", "max_output_bytes": 65536},
  "state": {"file": "state.json"},
  "history": {"memory": 10},
  "rules": [
    {"id": "deploy", "name": "部署", "match": {"subject_contains": "deploy"},
     "command": "printf '%s' \"$SN_SUBJECT\""}
  ]
}`

func loadApp(t *testing.T, dir, body string) (*App, *config.Config) {
	t.Helper()
	path := writeConf(t, dir, body)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, err := newApp("test", path, cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	t.Cleanup(func() { a.close() })
	return a, cfg
}

func testMessage() mail.Message {
	return mail.Message{
		UID:       1,
		Mailbox:   "INBOX",
		From:      "ci@example.com",
		Subject:   "deploy now",
		MessageID: "msg-1",
		Body:      "please deploy",
		Date:      time.Now(),
	}
}

func TestProcessMatchAndSnapshot(t *testing.T) {
	a, _ := loadApp(t, t.TempDir(), baseConf)

	result, err := a.Process(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !result.Matched || result.Status != "success" {
		t.Fatalf("result = %+v", result)
	}

	entries, total := a.history.List(10, 0)
	if total != 1 || len(entries) != 1 {
		t.Fatalf("history total=%d entries=%d", total, len(entries))
	}
	if entries[0].Stdout != "deploy now" || entries[0].RuleID != "deploy" {
		t.Fatalf("entry = %+v", entries[0])
	}

	snap := a.Snapshot()
	if snap.Version != "test" {
		t.Fatalf("version = %q", snap.Version)
	}
	if snap.Mail.Enabled {
		t.Fatal("mail should be disabled")
	}
	if len(snap.Rules) != 1 || snap.Rules[0].LastStatus != "success" || snap.Rules[0].LastRunAt == nil {
		t.Fatalf("rules = %+v", snap.Rules)
	}
	if snap.Counters.Polls != 0 {
		t.Fatalf("counters = %+v", snap.Counters)
	}
}

func TestProcessNoMatch(t *testing.T) {
	a, _ := loadApp(t, t.TempDir(), baseConf)
	msg := testMessage()
	msg.Subject = "something else"
	result, err := a.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Matched {
		t.Fatal("expected no match")
	}
	if _, total := a.history.List(10, 0); total != 0 {
		t.Fatalf("history total = %d", total)
	}
}

func TestReloadAppliesRules(t *testing.T) {
	dir := t.TempDir()
	a, _ := loadApp(t, dir, baseConf)

	updated := `{
      "server": {"addr": "127.0.0.1:8899"},
      "log": {"level": "error"},
      "mail": {"enabled": false},
      "rules": [
        {"id": "a", "match": {"subject_contains": "a"}, "command": "true"},
        {"id": "b", "match": {"body_contains": "b"}, "command": "true"}
      ]
    }`
	writeConf(t, dir, updated)
	a.reload()

	snap := a.Snapshot()
	if len(snap.Rules) != 2 {
		t.Fatalf("rules = %+v", snap.Rules)
	}
}

func TestReloadInvalidKeepsOldRules(t *testing.T) {
	dir := t.TempDir()
	a, _ := loadApp(t, dir, baseConf)

	writeConf(t, dir, `{"mail": {"enabled": false}, "rules": [`)
	a.reload()

	snap := a.Snapshot()
	if len(snap.Rules) != 1 || snap.Rules[0].ID != "deploy" {
		t.Fatalf("rules = %+v", snap.Rules)
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	dir := t.TempDir()
	body := `{
      "server": {"addr": "` + addr + `"},
      "log": {"level": "error"},
      "mail": {"enabled": false},
      "rules": []
    }`
	path := writeConf(t, dir, body)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "test", path, "")
	}()

	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				lastErr = nil
				break
			}
			lastErr = err
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("server did not start: %v", lastErr)
	}

	resp, err := http.Get(base + "/api/status")
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestSnapshotMasksUsername(t *testing.T) {
	body := `{
      "server": {"addr": "127.0.0.1:8899"},
      "log": {"level": "error"},
      "mail": {"enabled": true, "host": "imap.example.com", "username": "alice@example.com", "password": "secret"},
      "rules": []
    }`
	dir := t.TempDir()
	path := writeConf(t, dir, body)
	cfg, err := config.Load(path)
	if err != nil {
		// The mail config is invalid (real network not required) only if validation fails;
		// username/password/host are present, so it should load.
		t.Fatalf("Load: %v", err)
	}
	a, err := newApp("test", path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()

	snap := a.Snapshot()
	if snap.Mail.Username != "a***@example.com" {
		t.Fatalf("username = %q", snap.Mail.Username)
	}
}

type discardIMAPLogger struct{}

func (discardIMAPLogger) Printf(string, ...interface{}) {}

type literalReader struct{ *bytes.Reader }

func (r literalReader) Size() int64 { return int64(r.Len()) }

func TestEndToEndPollerExecutesCommand(t *testing.T) {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("user", "pass")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	mem.AddUser(user)

	imapSrv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
		Logger:       discardIMAPLogger{},
	})
	imapLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = imapSrv.Serve(imapLn) }()
	defer imapSrv.Close()

	raw := []byte("From: ci@example.com\r\nSubject: deploy now\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nplease deploy")
	if _, err := user.Append("INBOX", literalReader{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append: %v", err)
	}

	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := httpLn.Addr().String()
	httpLn.Close()

	imapHost, imapPort, err := net.SplitHostPort(imapLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	body := `{
      "server": {"addr": "` + httpAddr + `"},
      "log": {"level": "error"},
      "mail": {
        "enabled": true, "host": "` + imapHost + `", "port": ` + imapPort + `,
        "security": "none", "username": "user", "password": "pass",
        "mailbox": "INBOX", "poll_interval": "5s", "on_start": "all"
      },
      "execute": {"shell": "/bin/bash", "timeout": "5s"},
      "state": {"file": "state.json"},
      "history": {"memory": 10},
      "rules": [
        {"id": "deploy", "name": "部署", "match": {"subject_contains": "deploy"},
         "command": "printf '%s' \"$SN_SUBJECT\""}
      ]
    }`
	path := writeConf(t, dir, body)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "test", path, "") }()

	base := "http://" + httpAddr
	deadline := time.Now().Add(15 * time.Second)
	var entryID string
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/executions?limit=10")
		if err == nil {
			var list struct {
				Total int                      `json:"total"`
				Items []map[string]interface{} `json:"items"`
			}
			decErr := json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			if decErr == nil && list.Total >= 1 {
				entryID, _ = list.Items[0]["id"].(string)
				if list.Items[0]["status"] != "success" {
					t.Fatalf("execution status = %v", list.Items[0]["status"])
				}
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if entryID == "" {
		t.Fatal("execution did not appear in history")
	}

	resp, err := http.Get(base + "/api/executions/" + entryID)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Stdout string `json:"stdout"`
		RuleID string `json:"rule_id"`
		From   string `json:"from"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if detail.Stdout != "deploy now" || detail.RuleID != "deploy" {
		t.Fatalf("detail = %+v", detail)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunRejectsInsecureAddrOverride(t *testing.T) {
	path := writeConf(t, t.TempDir(), baseConf)
	err := Run(context.Background(), "test", path, "0.0.0.0:12345")
	if err == nil || !strings.Contains(err.Error(), "--addr") {
		t.Fatalf("err = %v", err)
	}
}
