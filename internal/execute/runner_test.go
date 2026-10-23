package execute

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/mail"
	"github.com/xiws/SimpleNotify/internal/rules"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testRunner(t *testing.T, timeout time.Duration, maxOutput int64, env map[string]string) *Runner {
	t.Helper()
	cfg := config.Execute{
		Shell:          "/bin/bash",
		Timeout:        config.Duration(timeout),
		WorkDir:        t.TempDir(),
		MaxOutputBytes: maxOutput,
		Env:            env,
	}
	r, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func testRule(t *testing.T, command string) *rules.Rule {
	t.Helper()
	e, err := rules.Compile([]config.Rule{{
		ID:      "r1",
		Name:    "测试规则",
		Match:   config.Match{SubjectContains: "x"},
		Command: command,
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return e.Rules()[0]
}

func testMessage() mail.Message {
	return mail.Message{
		UID:       42,
		Mailbox:   "INBOX",
		From:      "alice@example.com",
		To:        "bob@example.com",
		Subject:   "deploy now",
		MessageID: "msg-1",
		Body:      "line1\nline2",
		Date:      time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func TestRunSuccess(t *testing.T) {
	r := testRunner(t, 5*time.Second, 1<<20, nil)
	res := r.Run(context.Background(), testRule(t, "printf 'hello'"), testMessage())
	if res.Status != "success" || res.ExitCode != 0 || res.Stdout != "hello" {
		t.Fatalf("result = %+v", res)
	}
	if res.Duration <= 0 {
		t.Fatal("expected positive duration")
	}
}

func TestRunFailedExitCode(t *testing.T) {
	r := testRunner(t, 5*time.Second, 1<<20, nil)
	res := r.Run(context.Background(), testRule(t, "echo oops >&2; exit 3"), testMessage())
	if res.Status != "failed" || res.ExitCode != 3 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("stderr = %q", res.Stderr)
	}
}

func TestRunTimeout(t *testing.T) {
	r := testRunner(t, 150*time.Millisecond, 1<<20, nil)
	start := time.Now()
	res := r.Run(context.Background(), testRule(t, "sleep 10"), testMessage())
	elapsed := time.Since(start)
	if res.Status != "timeout" {
		t.Fatalf("status = %q, result = %+v", res.Status, res)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRunOutputTruncated(t *testing.T) {
	r := testRunner(t, 5*time.Second, 4, nil)
	res := r.Run(context.Background(), testRule(t, "printf 'abcdefghij'"), testMessage())
	if res.Stdout != "abcd" || !res.Truncated {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunEnvironment(t *testing.T) {
	r := testRunner(t, 5*time.Second, 1<<20, map[string]string{"CUSTOM": "custom-value"})
	rule := testRule(t, `printf '%s|%s|%s|%s' "$SN_SUBJECT" "$SN_FROM" "$CUSTOM" "$SN_RULE_NAME"`)
	res := r.Run(context.Background(), rule, testMessage())
	want := "deploy now|alice@example.com|custom-value|测试规则"
	if res.Stdout != want {
		t.Fatalf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestRunWorkDir(t *testing.T) {
	r := testRunner(t, 5*time.Second, 1<<20, nil)
	res := r.Run(context.Background(), testRule(t, "pwd"), testMessage())
	got := strings.TrimSpace(res.Stdout)
	want, err := filepath.EvalSymlinks(r.workDir)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", got, err)
	}
	if gotResolved != want {
		t.Fatalf("pwd = %q, want %q", gotResolved, want)
	}
}

func TestRunTemplateError(t *testing.T) {
	r := testRunner(t, 5*time.Second, 1<<20, nil)
	res := r.Run(context.Background(), testRule(t, "echo {{.DoesNotExist}}"), testMessage())
	if res.Status != "error" || res.Error == "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestNewMissingShell(t *testing.T) {
	_, err := New(config.Execute{
		Shell:          "/nonexistent/shell",
		Timeout:        config.Duration(time.Second),
		MaxOutputBytes: 1024,
	}, testLogger())
	if err == nil {
		t.Fatal("expected error")
	}
}
