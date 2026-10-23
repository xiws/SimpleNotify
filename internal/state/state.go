// Package state persists the per-mailbox IMAP UID cursor.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const version = 1

// Mailbox is the persisted cursor for one mailbox.
type Mailbox struct {
	UIDValidity uint32 `json:"uid_validity"`
	LastUID     uint32 `json:"last_uid"`
	// CatchUp is "unread" or "all" while an initial catch-up is still running.
	// It is cleared once the catch-up has completed.
	CatchUp   string    `json:"catch_up,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type fileData struct {
	Version   int                `json:"version"`
	Mailboxes map[string]Mailbox `json:"mailboxes"`
}

// Store is a concurrency-safe, atomically persisted UID cursor store.
type Store struct {
	path string

	mu   sync.Mutex
	data fileData
}

// Open loads the state file at path, creating an empty store when it does not
// exist. A corrupt file is backed up and replaced by an empty store.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("state file path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	s := &Store{path: path, data: fileData{Version: version, Mailboxes: map[string]Mailbox{}}}

	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil || s.data.Mailboxes == nil {
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if renameErr := os.Rename(path, backup); renameErr != nil {
			return nil, fmt.Errorf("state file is corrupt (%v) and could not be backed up: %w", err, renameErr)
		}
		slog.Warn("state file is corrupt, starting fresh", "path", path, "backup", backup, "error", err)
		s.data = fileData{Version: version, Mailboxes: map[string]Mailbox{}}
		return s, nil
	}
	if s.data.Mailboxes == nil {
		s.data.Mailboxes = map[string]Mailbox{}
	}
	if s.data.Version == 0 {
		s.data.Version = version
	}
	return s, nil
}

// Get returns the cursor for mailbox.
func (s *Store) Get(mailbox string) (Mailbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data.Mailboxes[mailbox]
	return m, ok
}

// Set stores the cursor for mailbox and persists the state file.
func (s *Store) Set(mailbox string, m Mailbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now().UTC()
	}
	s.data.Mailboxes[mailbox] = m
	return s.persistLocked()
}

// Reset stores a new baseline for mailbox (used on first run or UIDVALIDITY change).
func (s *Store) Reset(mailbox string, uidValidity, lastUID uint32) error {
	return s.Set(mailbox, Mailbox{UIDValidity: uidValidity, LastUID: lastUID})
}

// Snapshot returns a copy of all cursors.
func (s *Store) Snapshot() map[string]Mailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Mailbox, len(s.data.Mailboxes))
	for k, v := range s.data.Mailboxes {
		out[k] = v
	}
	return out
}

func (s *Store) persistLocked() error {
	s.data.Version = version
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
