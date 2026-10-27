package mail

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/state"
)

const (
	metaBatchSize = 100
	bodyBatchSize = 20
)

// ProcessResult reports what happened to a message.
type ProcessResult struct {
	Matched bool
	Status  string
}

// Processor handles a parsed message (rule matching + command execution).
type Processor interface {
	Process(ctx context.Context, msg Message) (ProcessResult, error)
}

// Counters tracks polling activity.
type Counters struct {
	Polls     int64
	Messages  int64
	Matched   int64
	Succeeded int64
	Failed    int64
	Skipped   int64
}

// Status is a snapshot of the poller state.
type Status struct {
	Enabled       bool
	Host          string
	Username      string
	Mailbox       string
	PollInterval  time.Duration
	OnStart       string
	MarkAsSeen    bool
	Connected     bool
	LastPollAt    time.Time
	LastSuccessAt time.Time
	NextPollAt    time.Time
	LastError     string
	UIDValidity   uint32
	LastUID       uint32
	Counters      Counters
}

// Poller polls an IMAP mailbox and feeds new messages to a Processor.
type Poller struct {
	dialer  Dialer
	state   *state.Store
	proc    Processor
	mailCfg func() config.Mail
	logger  *slog.Logger

	mu sync.Mutex
	st Status
}

// New creates a Poller. mailCfg is called on every cycle so configuration
// reloads take effect without restarting the process.
func New(dialer Dialer, st *state.Store, proc Processor, mailCfg func() config.Mail, logger *slog.Logger) *Poller {
	p := &Poller{
		dialer:  dialer,
		state:   st,
		proc:    proc,
		mailCfg: mailCfg,
		logger:  logger,
	}
	cfg := mailCfg()
	p.st = Status{
		Enabled:      cfg.IsEnabled(),
		Host:         cfg.Addr(),
		Username:     cfg.Username,
		Mailbox:      cfg.Mailbox,
		PollInterval: cfg.PollInterval.Duration(),
		OnStart:      cfg.OnStart,
		MarkAsSeen:   cfg.MarkAsSeen,
	}
	return p
}

// Run polls forever until ctx is cancelled. The first poll happens immediately.
func (p *Poller) Run(ctx context.Context) {
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := p.mailCfg()
		if !cfg.IsEnabled() {
			if !sleep(ctx, 5*time.Second) {
				return
			}
			continue
		}

		err := p.PollOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		var delay time.Duration
		if err != nil {
			p.setError(err)
			p.logger.Warn("imap poll failed", "error", err)
			if backoff == 0 {
				backoff = cfg.PollInterval.Duration()
			} else {
				backoff *= 2
			}
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			delay = backoff
		} else {
			backoff = 0
			p.markSuccess()
			delay = cfg.PollInterval.Duration()
		}

		p.setNextPoll(time.Now().Add(delay))
		ok := sleep(ctx, delay)
		p.setNextPoll(time.Time{})
		if !ok {
			return
		}
	}
}

// PollOnce performs a single poll cycle.
func (p *Poller) PollOnce(ctx context.Context) error {
	cfg := p.mailCfg()
	if !cfg.IsEnabled() {
		return nil
	}

	p.mu.Lock()
	p.st.Counters.Polls++
	p.st.LastPollAt = time.Now()
	p.st.Host = cfg.Addr()
	p.st.Username = cfg.Username
	p.st.Mailbox = cfg.Mailbox
	p.st.PollInterval = cfg.PollInterval.Duration()
	p.st.OnStart = cfg.OnStart
	p.st.MarkAsSeen = cfg.MarkAsSeen
	p.mu.Unlock()

	conn, err := p.dialer.Dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	p.setConnected(true)
	defer p.setConnected(false)

	mailbox := cfg.Mailbox
	validity := conn.UIDValidity()
	uidNext := conn.UIDNext()
	if uidNext == 0 {
		return fmt.Errorf("could not determine UIDNEXT for %s", mailbox)
	}

	st, hasState := p.state.Get(mailbox)
	validState := hasState && st.UIDValidity == validity
	var (
		uids      []uint32
		finalLast *uint32
		catchUp   string
	)

	switch {
	case validState && st.CatchUp != "":
		// A previous initial catch-up was interrupted. Continue the same
		// search instead of falling back to the "all new UIDs" path.
		catchUp = st.CatchUp
		uids, err = p.searchCatchUp(ctx, conn, catchUp)
		if err != nil {
			return fmt.Errorf("search %s: %w", mailbox, err)
		}
		uids = uidsGreaterThan(uids, st.LastUID)
		baseline := baselineFromUIDNext(uidNext)
		finalLast = &baseline
		p.setCursor(validity, st.LastUID)

	case !validState:
		if hasState {
			p.logger.Warn("imap UIDVALIDITY changed, rebuilding cursor",
				"mailbox", mailbox, "old", st.UIDValidity, "new", validity)
		}
		catchUp = cfg.OnStart
		if catchUp != "unread" && catchUp != "all" {
			baseline := baselineFromUIDNext(uidNext)
			if serr := p.state.Reset(mailbox, validity, baseline); serr != nil {
				p.logger.Warn("persist baseline cursor", "error", serr)
			}
			p.setCursor(validity, baseline)
			return nil
		}
		uids, err = p.searchCatchUp(ctx, conn, catchUp)
		if err != nil {
			return fmt.Errorf("search %s: %w", mailbox, err)
		}
		if serr := p.state.Set(mailbox, state.Mailbox{UIDValidity: validity, CatchUp: catchUp}); serr != nil {
			p.logger.Warn("persist catch-up cursor", "error", serr)
		}
		baseline := baselineFromUIDNext(uidNext)
		finalLast = &baseline
		p.setCursor(validity, 0)

	default:
		last := baselineFromUIDNext(uidNext)
		if st.LastUID >= last {
			p.setCursor(validity, st.LastUID)
			return nil
		}
		for u := st.LastUID + 1; u <= last; u++ {
			uids = append(uids, u)
		}
		p.setCursor(validity, st.LastUID)
	}

	last, err := p.processUIDs(ctx, conn, cfg, validity, mailbox, uids, catchUp)
	if err != nil {
		return err
	}
	if finalLast != nil && (last < *finalLast || catchUp != "") {
		p.advance(mailbox, validity, *finalLast, "")
	}
	return nil
}

func (p *Poller) searchCatchUp(ctx context.Context, conn Conn, mode string) ([]uint32, error) {
	if mode == "all" {
		return conn.SearchAll(ctx)
	}
	return conn.SearchUnseen(ctx)
}

func (p *Poller) processUIDs(ctx context.Context, conn Conn, cfg config.Mail, validity uint32, mailbox string, uids []uint32, catchUp string) (uint32, error) {
	var last uint32
	if len(uids) == 0 {
		return last, nil
	}
	uids = dedupeSorted(uids)

	for start := 0; start < len(uids); start += metaBatchSize {
		end := min(start+metaBatchSize, len(uids))
		chunk := uids[start:end]

		metas, err := conn.FetchMetas(ctx, chunk)
		if err != nil {
			return last, fmt.Errorf("fetch metadata for uids %v: %w", chunk, err)
		}
		byUID := make(map[uint32]Meta, len(metas))
		for _, meta := range metas {
			byUID[meta.UID] = meta
		}

		eligible := make([]Meta, 0, len(chunk))
		for _, uid := range chunk {
			meta, ok := byUID[uid]
			if !ok {
				p.logger.Warn("message metadata missing, skipping", "mailbox", mailbox, "uid", uid)
				p.countSkipped()
				p.advance(mailbox, validity, uid, catchUp)
				last = uid
				continue
			}
			if meta.Size > cfg.MaxMessageBytes {
				p.logger.Warn("message too large, skipping",
					"mailbox", mailbox, "uid", uid, "size", meta.Size, "max", cfg.MaxMessageBytes)
				p.countSkipped()
				p.advance(mailbox, validity, uid, catchUp)
				last = uid
				continue
			}
			eligible = append(eligible, meta)
		}

		for bStart := 0; bStart < len(eligible); bStart += bodyBatchSize {
			bEnd := min(bStart+bodyBatchSize, len(eligible))
			batch := eligible[bStart:bEnd]
			batchUIDs := make([]uint32, len(batch))
			for i, meta := range batch {
				batchUIDs[i] = meta.UID
			}

			bodies, err := conn.FetchBodies(ctx, batchUIDs, cfg.MarkAsSeen)
			if err != nil {
				return last, fmt.Errorf("fetch bodies for uids %v: %w", batchUIDs, err)
			}

			for _, meta := range batch {
				raw, ok := bodies[meta.UID]
				if !ok {
					p.logger.Warn("message body missing, skipping", "mailbox", mailbox, "uid", meta.UID)
					p.countFailed()
					p.advance(mailbox, validity, meta.UID, catchUp)
					last = meta.UID
					continue
				}

				msg, parseErr := Parse(raw, cfg.MaxBodyBytes)
				if parseErr != nil {
					p.logger.Warn("parse message failed, skipping", "mailbox", mailbox, "uid", meta.UID, "error", parseErr)
					p.countFailed()
					p.advance(mailbox, validity, meta.UID, catchUp)
					last = meta.UID
					continue
				}
				msg.UID = meta.UID
				msg.Mailbox = mailbox
				msg.Size = meta.Size

				result, procErr := p.proc.Process(ctx, msg)
				if procErr != nil {
					p.logger.Warn("process message failed", "mailbox", mailbox, "uid", meta.UID, "error", procErr)
				}
				p.countProcessed(result, procErr)

				p.advance(mailbox, validity, meta.UID, catchUp)
				last = meta.UID
			}
		}
	}
	return last, nil
}

// Status returns a consistent snapshot of the poller.
func (p *Poller) Status() Status {
	cfg := p.mailCfg()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.st
	out.Enabled = cfg.IsEnabled()
	out.Host = cfg.Addr()
	out.Username = cfg.Username
	out.Mailbox = cfg.Mailbox
	out.PollInterval = cfg.PollInterval.Duration()
	out.OnStart = cfg.OnStart
	out.MarkAsSeen = cfg.MarkAsSeen
	return out
}

func (p *Poller) advance(mailbox string, validity, uid uint32, catchUp string) {
	if err := p.state.Set(mailbox, state.Mailbox{UIDValidity: validity, LastUID: uid, CatchUp: catchUp}); err != nil {
		p.logger.Warn("persist UID cursor failed", "mailbox", mailbox, "uid", uid, "error", err)
	}
	p.setCursor(validity, uid)
}

func (p *Poller) setCursor(validity, lastUID uint32) {
	p.mu.Lock()
	p.st.UIDValidity = validity
	p.st.LastUID = lastUID
	p.mu.Unlock()
}

func (p *Poller) setConnected(connected bool) {
	p.mu.Lock()
	p.st.Connected = connected
	p.mu.Unlock()
}

func (p *Poller) setError(err error) {
	p.mu.Lock()
	if err != nil {
		p.st.LastError = err.Error()
	}
	p.mu.Unlock()
}

func (p *Poller) markSuccess() {
	p.mu.Lock()
	p.st.LastSuccessAt = time.Now()
	p.st.LastError = ""
	p.mu.Unlock()
}

func (p *Poller) setNextPoll(t time.Time) {
	p.mu.Lock()
	p.st.NextPollAt = t
	p.mu.Unlock()
}

func (p *Poller) countProcessed(result ProcessResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Counters.Messages++
	switch {
	case err != nil:
		p.st.Counters.Failed++
	case !result.Matched:
		p.st.Counters.Skipped++
	default:
		p.st.Counters.Matched++
		if result.Status == "success" {
			p.st.Counters.Succeeded++
		} else {
			p.st.Counters.Failed++
		}
	}
}

func (p *Poller) countSkipped() {
	p.mu.Lock()
	p.st.Counters.Messages++
	p.st.Counters.Skipped++
	p.mu.Unlock()
}

func (p *Poller) countFailed() {
	p.mu.Lock()
	p.st.Counters.Messages++
	p.st.Counters.Failed++
	p.mu.Unlock()
}

func baselineFromUIDNext(uidNext uint32) uint32 {
	if uidNext == 0 {
		return 0
	}
	return uidNext - 1
}

func uidsGreaterThan(uids []uint32, uid uint32) []uint32 {
	out := uids[:0]
	for _, u := range uids {
		if u > uid {
			out = append(out, u)
		}
	}
	return out
}

func dedupeSorted(uids []uint32) []uint32 {
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	out := uids[:0]
	for _, uid := range uids {
		if len(out) == 0 || out[len(out)-1] != uid {
			out = append(out, uid)
		}
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
