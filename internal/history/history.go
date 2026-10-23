// Package history stores execution results in memory and optionally on disk.
package history

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one command execution result.
type Entry struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	UID        uint32    `json:"uid"`
	Mailbox    string    `json:"mailbox"`
	From       string    `json:"from"`
	Subject    string    `json:"subject"`
	MessageID  string    `json:"message_id"`
	Command    string    `json:"command"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	Error      string    `json:"error"`
	Truncated  bool      `json:"truncated"`
}

// NewID returns a sortable, unique entry ID.
func NewID(t time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.UnixNano(), hex.EncodeToString(b[:]))
}

// Store is a bounded, concurrency-safe execution history.
type Store struct {
	mu      sync.RWMutex
	entries []Entry // oldest first
	max     int
	file    *os.File
	logger  *slog.Logger
}

// Open creates a history store. When file is not empty, entries are appended as
// JSONL and the most recent entries are loaded on startup.
func Open(memory int, file string, logger *slog.Logger) (*Store, error) {
	if memory < 1 {
		memory = 200
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{max: memory, logger: logger}

	if file == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	s.load(file)

	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open history file: %w", err)
	}
	s.file = f
	return s, nil
}

func (s *Store) load(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("read history file failed", "path", path, "error", err)
		}
		return
	}

	var loaded []Entry
	reader := bufio.NewReader(bytes.NewReader(b))
	bad := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				bad++
			} else {
				loaded = append(loaded, e)
			}
		}
		if readErr != nil {
			break
		}
	}
	if bad > 0 {
		s.logger.Warn("skipped corrupt history lines", "path", path, "count", bad)
	}
	if len(loaded) > s.max {
		loaded = loaded[len(loaded)-s.max:]
	}
	s.entries = loaded
}

// Add stores an entry, assigning an ID when empty.
func (s *Store) Add(e Entry) {
	if e.ID == "" {
		e.ID = NewID(e.StartedAt)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, e)
	if len(s.entries) > s.max {
		s.entries = append([]Entry(nil), s.entries[len(s.entries)-s.max:]...)
	}

	if s.file != nil {
		line, err := json.Marshal(e)
		if err != nil {
			s.logger.Warn("encode history entry failed", "error", err)
			return
		}
		line = append(line, '\n')
		if _, err := s.file.Write(line); err != nil {
			s.logger.Warn("append history entry failed", "error", err)
		}
	}
}

// List returns entries newest first.
func (s *Store) List(limit, offset int) ([]Entry, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.entries)
	if offset >= total {
		return []Entry{}, total
	}
	end := total - offset
	start := end - limit
	if start < 0 {
		start = 0
	}
	out := make([]Entry, end-start)
	for i := 0; i < len(out); i++ {
		out[i] = s.entries[end-1-i]
	}
	return out, total
}

// Get returns the entry with the given ID.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].ID == id {
			return s.entries[i], true
		}
	}
	return Entry{}, false
}

// LatestByRule returns the most recent entry for each rule ID.
func (s *Store) LatestByRule() map[string]Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Entry)
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if _, ok := out[e.RuleID]; !ok {
			out[e.RuleID] = e
		}
	}
	return out
}

// Close closes the backing file, if any.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
