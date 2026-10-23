// Package web serves the embedded admin page and its read-only JSON API.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/xiws/SimpleNotify/internal/history"
)

//go:embed assets/*
var assets embed.FS

// MailInfo is the admin-facing mail status.
type MailInfo struct {
	Enabled         bool       `json:"enabled"`
	Host            string     `json:"host"`
	Username        string     `json:"username"`
	Mailbox         string     `json:"mailbox"`
	PollInterval    string     `json:"poll_interval"`
	OnStart         string     `json:"on_start"`
	MarkAsSeen      bool       `json:"mark_as_seen"`
	SendID          bool       `json:"send_id"`
	MaxMessageBytes int64      `json:"max_message_bytes"`
	MaxBodyBytes    int64      `json:"max_body_bytes"`
	Connected       bool       `json:"connected"`
	LastPollAt      *time.Time `json:"last_poll_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	NextPollAt      *time.Time `json:"next_poll_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	UIDValidity     uint32     `json:"uid_validity"`
	LastUID         uint32     `json:"last_uid"`
}

// ExecuteInfo is the admin-facing executor configuration.
type ExecuteInfo struct {
	Shell   string `json:"shell"`
	Timeout string `json:"timeout"`
	WorkDir string `json:"work_dir"`
}

// Counters holds polling counters.
type Counters struct {
	Polls     int64 `json:"polls"`
	Messages  int64 `json:"messages"`
	Matched   int64 `json:"matched"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
}

// RuleInfo is the admin-facing rule summary.
type RuleInfo struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`
	Summary      string     `json:"summary"`
	Command      string     `json:"command"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastStatus   string     `json:"last_status,omitempty"`
	LastExitCode *int       `json:"last_exit_code,omitempty"`
}

// Snapshot is the data the server shows on the overview page.
type Snapshot struct {
	Version    string      `json:"version"`
	StartedAt  time.Time   `json:"started_at"`
	ConfigPath string      `json:"config_path"`
	Addr       string      `json:"addr"`
	Mail       MailInfo    `json:"mail"`
	Execute    ExecuteInfo `json:"execute"`
	Counters   Counters    `json:"counters"`
	Rules      []RuleInfo  `json:"rules"`
}

// ExecutionSummary is a history row without the command output.
type ExecutionSummary struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	UID        uint32    `json:"uid"`
	From       string    `json:"from"`
	Subject    string    `json:"subject"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	Truncated  bool      `json:"truncated"`
}

// ExecutionDetail is a full history entry including command output.
type ExecutionDetail struct {
	ExecutionSummary
	FinishedAt time.Time `json:"finished_at"`
	Mailbox    string    `json:"mailbox"`
	MessageID  string    `json:"message_id"`
	Command    string    `json:"command"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	Error      string    `json:"error"`
}

// Options wires the web server to the application.
type Options struct {
	Version  string
	Password func() string
	Snapshot func() Snapshot
	List     func(limit, offset int) ([]history.Entry, int)
	Get      func(id string) (history.Entry, bool)
	Logger   *slog.Logger
}

type server struct {
	opts       Options
	logger     *slog.Logger
	index      []byte
	fileServer http.Handler
}

// New builds the admin HTTP handler.
func New(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}

	s := &server{
		opts:       opts,
		logger:     logger,
		index:      index,
		fileServer: http.FileServerFS(sub),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", s.handleHealth)
	mux.Handle("GET /api/status", s.auth(http.HandlerFunc(s.handleStatus)))
	mux.Handle("GET /api/executions", s.auth(http.HandlerFunc(s.handleExecutions)))
	mux.Handle("GET /api/executions/{id}", s.auth(http.HandlerFunc(s.handleExecution)))
	mux.Handle("GET /assets/", s.auth(http.StripPrefix("/assets/", s.fileServer)))
	mux.Handle("GET /", s.auth(http.HandlerFunc(s.handleIndex)))

	return securityHeaders(mux)
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		password := ""
		if s.opts.Password != nil {
			password = s.opts.Password()
		}
		if password == "" {
			next.ServeHTTP(w, r)
			return
		}
		_, given, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="SimpleNotify", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, "未授权")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type statusResponse struct {
	Snapshot
	Recent []ExecutionSummary `json:"recent_executions"`
}

func (s *server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	resp := statusResponse{}
	if s.opts.Snapshot != nil {
		resp.Snapshot = s.opts.Snapshot()
	}
	if s.opts.List != nil {
		entries, _ := s.opts.List(10, 0)
		resp.Recent = summaries(entries)
	}
	if resp.Recent == nil {
		resp.Recent = []ExecutionSummary{}
	}
	if resp.Rules == nil {
		resp.Rules = []RuleInfo{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleExecutions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit 必须是正整数")
			return
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset 必须是非负整数")
			return
		}
		offset = n
	}

	var (
		entries []history.Entry
		total   int
	)
	if s.opts.List != nil {
		entries, total = s.opts.List(limit, offset)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total,
		"items": summaries(entries),
	})
}

func (s *server) handleExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.opts.Get == nil {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	entry, ok := s.opts.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	detail := ExecutionDetail{
		ExecutionSummary: summary(entry),
		FinishedAt:       entry.FinishedAt,
		Mailbox:          entry.Mailbox,
		MessageID:        entry.MessageID,
		Command:          entry.Command,
		Stdout:           entry.Stdout,
		Stderr:           entry.Stderr,
		Error:            entry.Error,
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		writeError(w, http.StatusNotFound, "页面不存在")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.index)
}

func summaries(entries []history.Entry) []ExecutionSummary {
	out := make([]ExecutionSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, summary(e))
	}
	return out
}

func summary(e history.Entry) ExecutionSummary {
	return ExecutionSummary{
		ID:         e.ID,
		StartedAt:  e.StartedAt,
		DurationMS: e.DurationMS,
		RuleID:     e.RuleID,
		RuleName:   e.RuleName,
		UID:        e.UID,
		From:       e.From,
		Subject:    e.Subject,
		Status:     e.Status,
		ExitCode:   e.ExitCode,
		Truncated:  e.Truncated,
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Warn("write json response failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
