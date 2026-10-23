package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Reset("INBOX", 111, 41); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	m, ok := s2.Get("INBOX")
	if !ok || m.UIDValidity != 111 || m.LastUID != 41 {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
}

func TestCorruptFileRecovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := s.Get("INBOX"); ok {
		t.Fatal("expected empty state")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("expected corrupt backup, got %v", matches)
	}
}

func TestConcurrentSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Set("INBOX", Mailbox{UIDValidity: 1, LastUID: uint32(i)}); err != nil {
				t.Errorf("Set: %v", err)
			}
		}(i)
	}
	wg.Wait()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := s2.Get("INBOX"); !ok || m.UIDValidity != 1 || m.LastUID >= 20 {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
}
