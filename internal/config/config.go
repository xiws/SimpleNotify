// Package config loads, defaults and validates the SimpleNotify configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration is a JSON-friendly time.Duration that accepts strings such as "30s".
type Duration time.Duration

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalJSON parses a duration string (for example "30s").
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON serializes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Config is the root configuration document.
type Config struct {
	Server  Server  `json:"server"`
	Log     Log     `json:"log"`
	Mail    Mail    `json:"mail"`
	Execute Execute `json:"execute"`
	State   State   `json:"state"`
	History History `json:"history"`
	Rules   []Rule  `json:"rules"`

	// Path is the absolute path of the loaded configuration file.
	Path string `json:"-"`
	// Dir is the directory containing the configuration file.
	Dir string `json:"-"`
}

// Server holds the admin web server settings.
type Server struct {
	Addr                string `json:"addr"`
	Password            string `json:"password"`
	AllowInsecureRemote bool   `json:"allow_insecure_remote"`
}

// Log holds logging settings.
type Log struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

// Mail holds the IMAP polling settings.
type Mail struct {
	Enabled            *bool    `json:"enabled"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Security           string   `json:"security"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify"`
	Username           string   `json:"username"`
	Password           string   `json:"password"`
	Mailbox            string   `json:"mailbox"`
	PollInterval       Duration `json:"poll_interval"`
	OnStart            string   `json:"on_start"`
	MarkAsSeen         bool     `json:"mark_as_seen"`
	SendID             *bool    `json:"send_id"`
	MaxMessageBytes    int64    `json:"max_message_bytes"`
	MaxBodyBytes       int64    `json:"max_body_bytes"`
}

// IsEnabled reports whether IMAP polling is enabled (default true).
func (m Mail) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// IsSendID reports whether the IMAP ID extension should be sent (default true).
func (m Mail) IsSendID() bool { return m.SendID == nil || *m.SendID }

// Addr returns host:port for the IMAP server.
func (m Mail) Addr() string { return net.JoinHostPort(m.Host, strconv.Itoa(m.Port)) }

// Execute holds command execution settings.
type Execute struct {
	Shell          string            `json:"shell"`
	Timeout        Duration          `json:"timeout"`
	WorkDir        string            `json:"work_dir"`
	MaxOutputBytes int64             `json:"max_output_bytes"`
	Env            map[string]string `json:"env"`
}

// State holds the UID cursor persistence settings.
type State struct {
	File string `json:"file"`
}

// History holds the execution history settings.
type History struct {
	File   string `json:"file"`
	Memory int    `json:"memory"`
}

// Rule maps a message match to a bash command.
type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
	Match   Match  `json:"match"`
	Command string `json:"command"`
}

// IsEnabled reports whether the rule is active (default true).
func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Match holds the rule conditions. All configured conditions must match.
type Match struct {
	From            string `json:"from"`
	SubjectContains string `json:"subject_contains"`
	SubjectRegex    string `json:"subject_regex"`
	BodyContains    string `json:"body_contains"`
	BodyRegex       string `json:"body_regex"`
}

// IsEmpty reports whether no condition is configured.
func (m Match) IsEmpty() bool {
	return m.From == "" && m.SubjectContains == "" && m.SubjectRegex == "" &&
		m.BodyContains == "" && m.BodyRegex == ""
}

// Load reads, defaults and validates the configuration file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config %s: unexpected data after JSON document", path)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %s: %w", path, err)
	}
	cfg.Path = abs
	cfg.Dir = filepath.Dir(abs)

	applyEnv(cfg)
	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SN_MAIL_PASSWORD"); v != "" {
		cfg.Mail.Password = v
	}
	if v := os.Getenv("SN_ADMIN_PASSWORD"); v != "" {
		cfg.Server.Password = v
	}
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:8899"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}

	m := &cfg.Mail
	if m.Security == "" {
		m.Security = "tls"
	}
	if m.Port == 0 {
		if m.Security == "tls" {
			m.Port = 993
		} else {
			m.Port = 143
		}
	}
	if m.Mailbox == "" {
		m.Mailbox = "INBOX"
	}
	if m.PollInterval == 0 {
		m.PollInterval = Duration(30 * time.Second)
	}
	if m.OnStart == "" {
		m.OnStart = "skip"
	}
	if m.MaxMessageBytes == 0 {
		m.MaxMessageBytes = 4 << 20
	}
	if m.MaxBodyBytes == 0 {
		m.MaxBodyBytes = 256 << 10
	}

	e := &cfg.Execute
	if e.Shell == "" {
		e.Shell = "/bin/bash"
	}
	if e.Timeout == 0 {
		e.Timeout = Duration(30 * time.Second)
	}
	if e.MaxOutputBytes == 0 {
		e.MaxOutputBytes = 64 << 10
	}
	if e.Env == nil {
		e.Env = map[string]string{}
	}

	if cfg.State.File == "" {
		cfg.State.File = "state.json"
	}
	if cfg.History.Memory == 0 {
		cfg.History.Memory = 200
	}

	for i := range cfg.Rules {
		if cfg.Rules[i].ID == "" {
			cfg.Rules[i].ID = fmt.Sprintf("rule-%d", i+1)
		}
		if cfg.Rules[i].Name == "" {
			cfg.Rules[i].Name = cfg.Rules[i].ID
		}
	}

	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cfg.Dir, p)
	}
	cfg.State.File = resolve(cfg.State.File)
	cfg.History.File = resolve(cfg.History.File)
	cfg.Log.File = resolve(cfg.Log.File)
	if e.WorkDir == "" {
		e.WorkDir = cfg.Dir
	} else {
		e.WorkDir = resolve(e.WorkDir)
	}
}

// Validate checks the configuration and returns all problems joined together.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := c.ValidateListenAddr(c.Server.Addr); err != nil {
		add("server.addr: %v", err)
	}

	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: must be one of debug, info, warn, error")
	}

	if c.Mail.IsEnabled() {
		if c.Mail.Host == "" {
			add("mail.host: required when mail.enabled is true")
		}
		if c.Mail.Port < 1 || c.Mail.Port > 65535 {
			add("mail.port: must be between 1 and 65535")
		}
		switch c.Mail.Security {
		case "tls", "starttls", "none":
		default:
			add("mail.security: must be one of tls, starttls, none")
		}
		if c.Mail.Username == "" {
			add("mail.username: required when mail.enabled is true")
		}
		if c.Mail.Password == "" {
			add("mail.password: required when mail.enabled is true (or set SN_MAIL_PASSWORD)")
		}
		if c.Mail.PollInterval.Duration() < 5*time.Second {
			add("mail.poll_interval: must be at least 5s")
		}
		switch c.Mail.OnStart {
		case "skip", "unread", "all":
		default:
			add("mail.on_start: must be one of skip, unread, all")
		}
		if c.Mail.MaxMessageBytes <= 0 {
			add("mail.max_message_bytes: must be positive")
		}
		if c.Mail.MaxBodyBytes <= 0 {
			add("mail.max_body_bytes: must be positive")
		}
	}

	if c.Execute.Shell == "" {
		add("execute.shell: required")
	}
	if c.Execute.Timeout.Duration() <= 0 {
		add("execute.timeout: must be positive")
	}
	if c.Execute.MaxOutputBytes <= 0 {
		add("execute.max_output_bytes: must be positive")
	}
	if c.State.File == "" {
		add("state.file: required")
	}
	if c.History.Memory < 1 {
		add("history.memory: must be at least 1")
	}

	seen := make(map[string]bool, len(c.Rules))
	for i, r := range c.Rules {
		prefix := fmt.Sprintf("rules[%d]", i)
		if r.ID == "" {
			add("%s.id: required", prefix)
		} else if seen[r.ID] {
			add("%s.id: duplicate rule id %q", prefix, r.ID)
		}
		seen[r.ID] = true
		if r.Match.IsEmpty() {
			add("%s.match: at least one condition is required", prefix)
			continue
		}
		if r.Match.SubjectRegex != "" {
			if _, err := regexp.Compile(r.Match.SubjectRegex); err != nil {
				add("%s.match.subject_regex: %v", prefix, err)
			}
		}
		if r.Match.BodyRegex != "" {
			if _, err := regexp.Compile(r.Match.BodyRegex); err != nil {
				add("%s.match.body_regex: %v", prefix, err)
			}
		}
		if strings.TrimSpace(r.Command) == "" {
			add("%s.command: required", prefix)
		}
	}

	return errors.Join(errs...)
}

// ValidateListenAddr validates a listen address, including the rule that a
// non-loopback address requires an admin password (or an explicit override).
// It is also used for the --addr CLI override.
func (c *Config) ValidateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %v", addr, err)
	}
	p, convErr := strconv.Atoi(port)
	if convErr != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	if !isLoopbackHost(host) && c.Server.Password == "" && !c.Server.AllowInsecureRemote {
		return fmt.Errorf("%q is not loopback and server.password is empty; set a password or server.allow_insecure_remote=true", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MaskUsername hides most of an email address for display (a***@example.com).
func MaskUsername(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		if len(s) <= 1 {
			return "*"
		}
		return s[:1] + "***"
	}
	local := s[:at]
	if len(local) <= 1 {
		local = "*"
	} else {
		local = local[:1] + "***"
	}
	return local + s[at:]
}
