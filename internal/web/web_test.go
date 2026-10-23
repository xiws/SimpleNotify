package web

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xiws/SimpleNotify/internal/history"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testOptions(password string) Options {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	lastRun := now.Add(time.Minute)
	exitCode := 0
	entry := history.Entry{
		ID:         "e1",
		StartedAt:  now,
		FinishedAt: now.Add(12 * time.Millisecond),
		DurationMS: 12,
		RuleID:     "r1",
		RuleName:   "测试规则",
		UID:        7,
		Mailbox:    "INBOX",
		From:       "alice@example.com",
		Subject:    "hello",
		MessageID:  "msg-1",
		Command:    "echo hi",
		Status:     "success",
		ExitCode:   0,
		Stdout:     "hi",
		Stderr:     "",
	}
	return Options{
		Version:  "test",
		Password: func() string { return password },
		Snapshot: func() Snapshot {
			return Snapshot{
				Version:    "test",
				StartedAt:  now,
				ConfigPath: "/tmp/conf.json",
				Addr:       "127.0.0.1:8899",
				Mail: MailInfo{
					Enabled:      true,
					Host:         "imap.example.com:993",
					Username:     "u***@example.com",
					Mailbox:      "INBOX",
					PollInterval: "30s",
					Connected:    true,
				},
				Execute:  ExecuteInfo{Shell: "/bin/bash", Timeout: "30s", WorkDir: "/tmp"},
				Counters: Counters{Polls: 1, Messages: 2, Matched: 1, Succeeded: 0, Failed: 1},
				Rules: []RuleInfo{{
					ID:           "r1",
					Name:         "测试规则",
					Enabled:      true,
					Summary:      "主题包含 \"hello\"",
					Command:      "echo hi",
					LastRunAt:    &lastRun,
					LastStatus:   "success",
					LastExitCode: &exitCode,
				}},
			}
		},
		List: func(limit, offset int) ([]history.Entry, int) {
			if offset > 0 {
				return nil, 1
			}
			return []history.Entry{entry}, 1
		},
		Get: func(id string) (history.Entry, bool) {
			if id == entry.ID {
				return entry, true
			}
			return history.Entry{}, false
		},
		Logger: testLogger(),
	}
}

func do(handler http.Handler, method, path, user, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if user != "" || password != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestIndexAndAssets(t *testing.T) {
	h := New(testOptions(""))
	rec := do(h, http.MethodGet, "/", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SimpleNotify 管理台") {
		t.Fatalf("body missing title: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}

	rec = do(h, http.MethodGet, "/assets/style.css", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "css") {
		t.Fatalf("asset status=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = do(h, http.MethodGet, "/nope", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", rec.Code)
	}
}

func TestHealthzPublic(t *testing.T) {
	h := New(testOptions("secret"))
	rec := do(h, http.MethodGet, "/api/healthz", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuth(t *testing.T) {
	h := New(testOptions("secret"))

	rec := do(h, http.MethodGet, "/api/status", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "SimpleNotify") {
		t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
	}

	rec = do(h, http.MethodGet, "/api/status", "any", "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d", rec.Code)
	}

	rec = do(h, http.MethodGet, "/api/status", "admin", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct password status = %d", rec.Code)
	}

	// The index requires auth too when a password is set.
	rec = do(h, http.MethodGet, "/", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("index status = %d", rec.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	h := New(testOptions(""))
	rec := do(h, http.MethodGet, "/api/status", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Version          string             `json:"version"`
		Mail             MailInfo           `json:"mail"`
		Rules            []RuleInfo         `json:"rules"`
		RecentExecutions []ExecutionSummary `json:"recent_executions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "test" || body.Mail.Username != "u***@example.com" {
		t.Fatalf("body = %+v", body)
	}
	if len(body.Rules) != 1 || body.Rules[0].ID != "r1" {
		t.Fatalf("rules = %+v", body.Rules)
	}
	if len(body.RecentExecutions) != 1 || body.RecentExecutions[0].ID != "e1" {
		t.Fatalf("recent = %+v", body.RecentExecutions)
	}
}

func TestExecutionsEndpoints(t *testing.T) {
	h := New(testOptions(""))

	rec := do(h, http.MethodGet, "/api/executions?limit=10&offset=0", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var list struct {
		Total int                `json:"total"`
		Items []ExecutionSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].RuleID != "r1" {
		t.Fatalf("list = %+v", list)
	}

	rec = do(h, http.MethodGet, "/api/executions?limit=0", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit status = %d", rec.Code)
	}

	rec = do(h, http.MethodGet, "/api/executions/e1", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "stdout") {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail ExecutionDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Command != "echo hi" || detail.Stdout != "hi" || detail.UID != 7 {
		t.Fatalf("detail = %+v", detail)
	}

	rec = do(h, http.MethodGet, "/api/executions/missing", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}
}

func TestReadOnlyMethods(t *testing.T) {
	h := New(testOptions(""))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(h, method, "/api/status", "", "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, rec.Code)
		}
	}
}
