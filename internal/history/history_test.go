package history

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func entry(id, rule, status string, t time.Time) Entry {
	return Entry{
		ID:         id,
		RuleID:     rule,
		RuleName:   rule,
		Status:     status,
		StartedAt:  t,
		FinishedAt: t.Add(time.Second),
		DurationMS: 1000,
		Subject:    "subject-" + id,
	}
}

func TestListNewestFirstAndBounded(t *testing.T) {
	s, err := Open(3, "", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	s.Add(entry("1", "a", "success", base))
	s.Add(entry("2", "a", "failed", base.Add(time.Second)))
	s.Add(entry("3", "b", "success", base.Add(2*time.Second)))
	s.Add(entry("4", "b", "success", base.Add(3*time.Second)))

	items, total := s.List(10, 0)
	if total != 3 || len(items) != 3 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
	if items[0].ID != "4" || items[2].ID != "2" {
		t.Fatalf("order = %v", []string{items[0].ID, items[1].ID, items[2].ID})
	}

	page, total := s.List(1, 1)
	if total != 3 || len(page) != 1 || page[0].ID != "3" {
		t.Fatalf("page = %+v total=%d", page, total)
	}

	empty, _ := s.List(10, 99)
	if len(empty) != 0 {
		t.Fatalf("expected empty page, got %+v", empty)
	}
}

func TestGetAndLatestByRule(t *testing.T) {
	s, err := Open(10, "", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	s.Add(entry("1", "a", "success", base))
	s.Add(entry("2", "b", "failed", base.Add(time.Second)))
	s.Add(entry("3", "a", "success", base.Add(2*time.Second)))

	if got, ok := s.Get("2"); !ok || got.RuleID != "b" {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("expected miss")
	}

	latest := s.LatestByRule()
	if latest["a"].ID != "3" || latest["b"].ID != "2" {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := Open(10, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i, id := range []string{"1", "2", "3"} {
		s.Add(entry(id, "r", "success", base.Add(time.Duration(i)*time.Second)))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	s2, err := Open(2, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	items, total := s2.List(10, 0)
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
	if items[0].ID != "3" || items[1].ID != "2" {
		t.Fatalf("loaded = %+v", items)
	}
}

func TestCorruptLinesSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"+`{"id":"ok","rule_id":"r"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(10, path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, total := s.List(10, 0)
	if total != 1 || items[0].ID != "ok" {
		t.Fatalf("total=%d items=%+v", total, items)
	}
}

func TestConcurrentAddAndList(t *testing.T) {
	s, err := Open(50, "", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(entry("", "r", "success", time.Now()))
			s.List(10, 0)
			s.LatestByRule()
		}()
	}
	wg.Wait()
	if _, total := s.List(100, 0); total != 20 {
		t.Fatalf("total = %d", total)
	}
}
