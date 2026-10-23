package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimalConfig = `{
  "mail": {"enabled": false},
  "rules": [
    {"id": "hello", "match": {"subject_contains": "hello"}, "command": "echo hi"}
  ]
}`

func TestLoadDefaultsAndPaths(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:8899" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Mail.Security != "tls" || cfg.Mail.Port != 993 {
		t.Errorf("mail security/port = %q/%d", cfg.Mail.Security, cfg.Mail.Port)
	}
	if cfg.Mail.Mailbox != "INBOX" {
		t.Errorf("mailbox = %q", cfg.Mail.Mailbox)
	}
	if cfg.Execute.Shell != "/bin/bash" {
		t.Errorf("shell = %q", cfg.Execute.Shell)
	}
	if got := cfg.Execute.WorkDir; got != cfg.Dir {
		t.Errorf("workdir = %q, want config dir %q", got, cfg.Dir)
	}
	if got := cfg.State.File; got != filepath.Join(cfg.Dir, "state.json") {
		t.Errorf("state file = %q", got)
	}
	if cfg.History.Memory != 200 {
		t.Errorf("history memory = %d", cfg.History.Memory)
	}
	if !cfg.Rules[0].IsEnabled() {
		t.Error("rule should default to enabled")
	}
	if cfg.Mail.IsEnabled() {
		t.Error("mail should be disabled")
	}
}

func TestLoadUnknownField(t *testing.T) {
	path := writeConfig(t, `{"mail": {"enabled": false}, "nope": 1}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadEnvPassword(t *testing.T) {
	path := writeConfig(t, `{
      "mail": {"enabled": true, "host": "imap.example.com", "username": "u@example.com"},
      "rules": []
    }`)
	t.Setenv("SN_MAIL_PASSWORD", "from-env")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mail.Password != "from-env" {
		t.Fatalf("password = %q", cfg.Mail.Password)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing mail password",
			body: `{"mail":{"enabled":true,"host":"h","username":"u"},"rules":[]}`,
			want: "mail.password",
		},
		{
			name: "remote without password",
			body: `{"server":{"addr":"0.0.0.0:8899"},"mail":{"enabled":false},"rules":[]}`,
			want: "server.password",
		},
		{
			name: "duplicate ids",
			body: `{"mail":{"enabled":false},"rules":[
              {"id":"a","match":{"subject_contains":"x"},"command":"true"},
              {"id":"a","match":{"body_contains":"x"},"command":"true"}]}`,
			want: "duplicate rule id",
		},
		{
			name: "empty match",
			body: `{"mail":{"enabled":false},"rules":[{"id":"a","match":{},"command":"true"}]}`,
			want: "at least one condition",
		},
		{
			name: "invalid regex",
			body: `{"mail":{"enabled":false},"rules":[{"id":"a","match":{"subject_regex":"("},"command":"true"}]}`,
			want: "subject_regex",
		},
		{
			name: "short interval",
			body: `{"mail":{"enabled":true,"host":"h","username":"u","password":"p","poll_interval":"1s"},"rules":[]}`,
			want: "mail.poll_interval",
		},
		{
			name: "bad on_start",
			body: `{"mail":{"enabled":true,"host":"h","username":"u","password":"p","on_start":"nope"},"rules":[]}`,
			want: "mail.on_start",
		},
		{
			name: "bad security",
			body: `{"mail":{"enabled":true,"host":"h","username":"u","password":"p","security":"ssl"},"rules":[]}`,
			want: "mail.security",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.body)
			_, err := Load(path)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRemoteWithAllowInsecure(t *testing.T) {
	path := writeConfig(t, `{"server":{"addr":"0.0.0.0:8899","allow_insecure_remote":true},"mail":{"enabled":false}}`)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestDurationParsing(t *testing.T) {
	path := writeConfig(t, `{"execute":{"timeout":"90s"},"mail":{"enabled":false}}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Execute.Timeout.Duration() != 90*time.Second {
		t.Fatalf("timeout = %v", cfg.Execute.Timeout)
	}
}

func TestMaskUsername(t *testing.T) {
	cases := map[string]string{
		"zhong@163.com": "z***@163.com",
		"a@b.c":         "*@b.c",
		"noatsign":      "n***",
		"":              "*",
	}
	for in, want := range cases {
		if got := MaskUsername(in); got != want {
			t.Errorf("MaskUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadTrailingData(t *testing.T) {
	path := writeConfig(t, `{"mail":{"enabled":false}} {"extra":true}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unexpected data") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateListenAddr(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ValidateListenAddr("127.0.0.1:8899"); err != nil {
		t.Fatalf("loopback should be allowed: %v", err)
	}
	if err := cfg.ValidateListenAddr("0.0.0.0:8899"); err == nil {
		t.Fatal("public bind without password should be rejected")
	}
	if err := cfg.ValidateListenAddr("nope"); err == nil {
		t.Fatal("invalid address should be rejected")
	}
	if err := cfg.ValidateListenAddr("127.0.0.1:0"); err == nil {
		t.Fatal("port 0 should be rejected")
	}

	cfg.Server.Password = "secret"
	if err := cfg.ValidateListenAddr("0.0.0.0:8899"); err != nil {
		t.Fatalf("public bind with password should be allowed: %v", err)
	}

	cfg.Server.Password = ""
	cfg.Server.AllowInsecureRemote = true
	if err := cfg.ValidateListenAddr("0.0.0.0:8899"); err != nil {
		t.Fatalf("explicit insecure override should be allowed: %v", err)
	}
}
