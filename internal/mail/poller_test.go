package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/state"
)

type fakeDialer struct {
	fn func(context.Context, config.Mail) (Conn, error)
}

func (d *fakeDialer) Dial(ctx context.Context, cfg config.Mail) (Conn, error) {
	return d.fn(ctx, cfg)
}

type fakeConn struct {
	validity uint32
	next     uint32

	unseen []uint32
	all    []uint32

	metas  map[uint32]Meta
	bodies map[uint32][]byte

	searchErr error
	metaErr   error
	bodyErr   error

	metaBatches [][]uint32
	closed      bool
	peeked      bool
}

func (c *fakeConn) UIDValidity() uint32 { return c.validity }
func (c *fakeConn) UIDNext() uint32     { return c.next }

func (c *fakeConn) SearchUnseen(context.Context) ([]uint32, error) {
	return c.unseen, c.searchErr
}

func (c *fakeConn) SearchAll(context.Context) ([]uint32, error) {
	return c.all, c.searchErr
}

func (c *fakeConn) FetchMetas(_ context.Context, uids []uint32) ([]Meta, error) {
	c.metaBatches = append(c.metaBatches, append([]uint32(nil), uids...))
	if c.metaErr != nil {
		return nil, c.metaErr
	}
	out := make([]Meta, 0, len(uids))
	for _, uid := range uids {
		if meta, ok := c.metas[uid]; ok {
			out = append(out, meta)
		}
	}
	return out, nil
}

func (c *fakeConn) FetchBodies(_ context.Context, uids []uint32, peek bool) (map[uint32][]byte, error) {
	c.peeked = peek
	if c.bodyErr != nil {
		return nil, c.bodyErr
	}
	out := make(map[uint32][]byte, len(uids))
	for _, uid := range uids {
		if body, ok := c.bodies[uid]; ok {
			out[uid] = body
		}
	}
	return out, nil
}

func (c *fakeConn) Close() error { c.closed = true; return nil }

type fakeProcessor struct {
	got    []Message
	result ProcessResult
	err    error
}

func (p *fakeProcessor) Process(_ context.Context, msg Message) (ProcessResult, error) {
	p.got = append(p.got, msg)
	return p.result, p.err
}

func testMail() config.Mail {
	return config.Mail{
		Host:            "imap.example.com",
		Port:            993,
		Security:        "tls",
		Username:        "u@example.com",
		Password:        "p",
		Mailbox:         "INBOX",
		PollInterval:    config.Duration(30 * time.Second),
		OnStart:         "skip",
		MaxMessageBytes: 4 << 20,
		MaxBodyBytes:    256 << 10,
	}
}

func newTestPoller(t *testing.T, conn *fakeConn, proc Processor, cfg config.Mail) (*Poller, *state.Store) {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dialer := &fakeDialer{fn: func(context.Context, config.Mail) (Conn, error) { return conn, nil }}
	return New(dialer, st, proc, func() config.Mail { return cfg }, logger), st
}

func rawMail(subject, body string) []byte {
	return []byte("From: a@example.com\r\nSubject: " + subject +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body)
}

func TestPollOnceSkipBaselines(t *testing.T) {
	conn := &fakeConn{validity: 7, next: 10}
	proc := &fakeProcessor{}
	p, st := newTestPoller(t, conn, proc, testMail())

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	m, ok := st.Get("INBOX")
	if !ok || m.UIDValidity != 7 || m.LastUID != 9 {
		t.Fatalf("state = %+v ok=%v", m, ok)
	}
	if len(proc.got) != 0 {
		t.Fatalf("processed %d messages, want 0", len(proc.got))
	}
	if !conn.closed {
		t.Fatal("connection not closed")
	}
}

func TestPollOnceProcessesNewMessages(t *testing.T) {
	conn := &fakeConn{
		validity: 1,
		next:     8,
		metas: map[uint32]Meta{
			6: {UID: 6, Size: 100},
			7: {UID: 7, Size: 200},
		},
		bodies: map[uint32][]byte{
			6: rawMail("first", "one"),
			7: rawMail("second", "two"),
		},
	}
	proc := &fakeProcessor{result: ProcessResult{Matched: true, Status: "success"}}
	p, st := newTestPoller(t, conn, proc, testMail())
	if err := st.Reset("INBOX", 1, 5); err != nil {
		t.Fatal(err)
	}

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(proc.got) != 2 || proc.got[0].Subject != "first" || proc.got[1].Subject != "second" {
		t.Fatalf("processed = %+v", proc.got)
	}
	if proc.got[0].UID != 6 || proc.got[0].Body != "one" {
		t.Fatalf("first message = %+v", proc.got[0])
	}
	m, _ := st.Get("INBOX")
	if m.LastUID != 7 {
		t.Fatalf("last uid = %d", m.LastUID)
	}
	status := p.Status()
	if status.Counters.Messages != 2 || status.Counters.Matched != 2 || status.Counters.Succeeded != 2 {
		t.Fatalf("counters = %+v", status.Counters)
	}
	if conn.peeked {
		t.Fatal("expected Peek=true when mark_as_seen is false")
	}
}

func TestPollOnceUIDValidityChangeRebaselines(t *testing.T) {
	conn := &fakeConn{validity: 2, next: 4}
	proc := &fakeProcessor{}
	p, st := newTestPoller(t, conn, proc, testMail())
	if err := st.Reset("INBOX", 1, 99); err != nil {
		t.Fatal(err)
	}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	m, _ := st.Get("INBOX")
	if m.UIDValidity != 2 || m.LastUID != 3 {
		t.Fatalf("state = %+v", m)
	}
	if len(proc.got) != 0 {
		t.Fatalf("processed %d messages", len(proc.got))
	}
}

func TestPollOnceOnStartUnread(t *testing.T) {
	conn := &fakeConn{
		validity: 3,
		next:     10,
		unseen:   []uint32{2, 4},
		metas: map[uint32]Meta{
			2: {UID: 2, Size: 10},
			4: {UID: 4, Size: 10},
		},
		bodies: map[uint32][]byte{
			2: rawMail("old-1", "a"),
			4: rawMail("old-2", "b"),
		},
	}
	proc := &fakeProcessor{result: ProcessResult{Matched: true, Status: "success"}}
	cfg := testMail()
	cfg.OnStart = "unread"
	p, st := newTestPoller(t, conn, proc, cfg)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(proc.got) != 2 {
		t.Fatalf("processed = %d", len(proc.got))
	}
	m, _ := st.Get("INBOX")
	if m.UIDValidity != 3 || m.LastUID != 9 || m.CatchUp != "" {
		t.Fatalf("state = %+v", m)
	}
}

func TestCatchUpSurvivesTransientFetchError(t *testing.T) {
	cfg := testMail()
	cfg.OnStart = "unread"

	bodies := map[uint32][]byte{
		2: rawMail("old-1", "a"),
		4: rawMail("old-2", "b"),
	}
	metas := map[uint32]Meta{
		2: {UID: 2, Size: 10},
		4: {UID: 4, Size: 10},
	}
	conn1 := &fakeConn{
		validity: 1, next: 10, unseen: []uint32{2, 4},
		metas: metas, bodies: bodies, bodyErr: errors.New("transient fetch error"),
	}
	conn2 := &fakeConn{
		validity: 1, next: 10, unseen: []uint32{2, 4},
		metas: metas, bodies: bodies,
	}

	calls := 0
	dialer := &fakeDialer{fn: func(context.Context, config.Mail) (Conn, error) {
		calls++
		if calls == 1 {
			return conn1, nil
		}
		return conn2, nil
	}}
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	proc := &fakeProcessor{result: ProcessResult{Matched: true, Status: "success"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(dialer, st, proc, func() config.Mail { return cfg }, logger)

	if err := p.PollOnce(context.Background()); err == nil {
		t.Fatal("expected transient error")
	}
	m, ok := st.Get("INBOX")
	if !ok || m.CatchUp != "unread" || m.LastUID != 0 {
		t.Fatalf("state after error = %+v ok=%v", m, ok)
	}

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("second PollOnce: %v", err)
	}
	if len(conn2.metaBatches) != 1 {
		t.Fatalf("meta batches = %v", conn2.metaBatches)
	}
	got := conn2.metaBatches[0]
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("retried UIDs = %v, want [2 4] (must not fall back to all new UIDs)", got)
	}
	if len(proc.got) != 2 {
		t.Fatalf("processed %d messages", len(proc.got))
	}
	m, _ = st.Get("INBOX")
	if m.CatchUp != "" || m.LastUID != 9 {
		t.Fatalf("state after retry = %+v", m)
	}
	if got := p.Status().Counters.Skipped; got != 0 {
		t.Fatalf("skipped = %d, want 0", got)
	}
}

func TestPollOnceSkipsOversizeMessage(t *testing.T) {
	conn := &fakeConn{
		validity: 1,
		next:     3,
		metas: map[uint32]Meta{
			2: {UID: 2, Size: 10 << 20},
		},
	}
	proc := &fakeProcessor{}
	cfg := testMail()
	cfg.MaxMessageBytes = 1 << 20
	p, st := newTestPoller(t, conn, proc, cfg)
	if err := st.Reset("INBOX", 1, 1); err != nil {
		t.Fatal(err)
	}

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(proc.got) != 0 {
		t.Fatalf("processed %d messages", len(proc.got))
	}
	m, _ := st.Get("INBOX")
	if m.LastUID != 2 {
		t.Fatalf("last uid = %d", m.LastUID)
	}
	if got := p.Status().Counters.Skipped; got != 1 {
		t.Fatalf("skipped = %d", got)
	}
}

func TestPollOnceMissingBodyAdvances(t *testing.T) {
	conn := &fakeConn{
		validity: 1,
		next:     3,
		metas: map[uint32]Meta{
			2: {UID: 2, Size: 10},
		},
		bodies: map[uint32][]byte{},
	}
	proc := &fakeProcessor{}
	p, st := newTestPoller(t, conn, proc, testMail())
	if err := st.Reset("INBOX", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	m, _ := st.Get("INBOX")
	if m.LastUID != 2 {
		t.Fatalf("last uid = %d", m.LastUID)
	}
	if got := p.Status().Counters.Failed; got != 1 {
		t.Fatalf("failed = %d", got)
	}
}

func TestPollOnceProcessorErrorStillAdvances(t *testing.T) {
	conn := &fakeConn{
		validity: 1,
		next:     3,
		metas:    map[uint32]Meta{2: {UID: 2, Size: 10}},
		bodies:   map[uint32][]byte{2: rawMail("x", "y")},
	}
	proc := &fakeProcessor{err: errors.New("boom")}
	p, st := newTestPoller(t, conn, proc, testMail())
	if err := st.Reset("INBOX", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	m, _ := st.Get("INBOX")
	if m.LastUID != 2 {
		t.Fatalf("last uid = %d", m.LastUID)
	}
	status := p.Status()
	if status.Counters.Failed != 1 || status.Counters.Messages != 1 {
		t.Fatalf("counters = %+v", status.Counters)
	}
}

func TestPollOnceDialError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	dialer := &fakeDialer{fn: func(context.Context, config.Mail) (Conn, error) {
		return nil, errors.New("dial failed")
	}}
	cfg := testMail()
	p := New(dialer, st, &fakeProcessor{}, func() config.Mail { return cfg }, logger)

	if err := p.PollOnce(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if p.Status().Counters.Polls != 1 {
		t.Fatalf("polls = %d", p.Status().Counters.Polls)
	}
}

func TestPollOnceDisabledSkipsDial(t *testing.T) {
	disabled := false
	conn := &fakeConn{}
	proc := &fakeProcessor{}
	cfg := testMail()
	cfg.Enabled = &disabled
	p, _ := newTestPoller(t, conn, proc, cfg)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if conn.closed {
		t.Fatal("connection should not be opened when disabled")
	}
	if p.Status().Counters.Polls != 0 {
		t.Fatal("polls should not be counted when disabled")
	}
}

func TestDedupeSorted(t *testing.T) {
	got := dedupeSorted([]uint32{3, 1, 3, 2, 2})
	want := []uint32{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
