package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conf.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConf = `{
  "log": {"level": "error"},
  "mail": {"enabled": false},
  "rules": [
    {"id": "a", "match": {"subject_contains": "x"}, "command": "true"}
  ]
}`

func TestValidateCommand(t *testing.T) {
	path := writeTestConfig(t, validConf)

	var out bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"validate", "--config", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\n%s", err, out.String())
	}
	for _, want := range []string{"配置有效", path, "邮箱: 未启用", "规则: 1 条（启用 1）"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestValidateMissingShell(t *testing.T) {
	path := writeTestConfig(t, `{"mail": {"enabled": false}, "execute": {"shell": "/nonexistent/shell"}, "rules": []}`)
	root := newRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"validate", "--config", path})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "execute.shell") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateMissingConfig(t *testing.T) {
	root := newRoot()
	root.SetArgs([]string{"validate", "--config", filepath.Join(t.TempDir(), "nope.json")})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateInvalidConfig(t *testing.T) {
	path := writeTestConfig(t, `{"mail": {"enabled": false}, "rules": [{"id": "a", "match": {}, "command": "true"}]}`)
	root := newRoot()
	root.SetArgs([]string{"validate", "--config", path})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least one condition") {
		t.Fatalf("err = %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "notify dev") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSendCommandNotImplemented(t *testing.T) {
	root := newRoot()
	root.SetArgs([]string{"send"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckMailRequiresEnabledMail(t *testing.T) {
	path := writeTestConfig(t, validConf)
	root := newRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"validate", "--config", path, "--check-mail"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "mail.enabled=true") {
		t.Fatalf("err = %v", err)
	}
}
