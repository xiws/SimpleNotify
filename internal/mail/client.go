package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/xiws/SimpleNotify/internal/config"
)

// Message is a parsed email ready for rule matching.
type Message struct {
	UID           uint32
	Mailbox       string
	From          string
	To            string
	Subject       string
	Date          time.Time
	MessageID     string
	Body          string
	Size          int64
	BodyTruncated bool
}

// Meta is the lightweight FETCH result used to pre-filter large messages.
type Meta struct {
	UID  uint32
	Size int64
}

// Conn is a logged-in, selected IMAP connection.
type Conn interface {
	UIDValidity() uint32
	UIDNext() uint32
	SearchUnseen(ctx context.Context) ([]uint32, error)
	SearchAll(ctx context.Context) ([]uint32, error)
	FetchMetas(ctx context.Context, uids []uint32) ([]Meta, error)
	FetchBodies(ctx context.Context, uids []uint32, peek bool) (map[uint32][]byte, error)
	Close() error
}

// Dialer opens IMAP connections.
type Dialer interface {
	Dial(ctx context.Context, cfg config.Mail) (Conn, error)
}

// CheckInfo is the result of a real IMAP login check.
type CheckInfo struct {
	Server       string
	Capabilities []string
	Mailbox      string
	UIDValidity  uint32
	UIDNext      uint32
	Messages     uint32
}

// Check logs in to the configured IMAP server and selects the mailbox.
func Check(ctx context.Context, cfg config.Mail, version string) (CheckInfo, error) {
	c, err := connect(ctx, cfg, version, slog.Default())
	if err != nil {
		return CheckInfo{}, err
	}
	defer c.Close()

	info := CheckInfo{Server: cfg.Addr(), Mailbox: cfg.Mailbox}
	caps, err := c.Capability().Wait()
	if err != nil {
		return info, fmt.Errorf("capability: %w", err)
	}
	for cap := range caps {
		info.Capabilities = append(info.Capabilities, string(cap))
	}
	sort.Strings(info.Capabilities)

	data, err := c.Select(cfg.Mailbox, &imap.SelectOptions{ReadOnly: !cfg.MarkAsSeen}).Wait()
	if err != nil {
		return info, fmt.Errorf("select %s: %w", cfg.Mailbox, err)
	}
	info.UIDValidity = data.UIDValidity
	info.UIDNext = uint32(data.UIDNext)
	info.Messages = data.NumMessages
	return info, nil
}

// NewDialer returns an IMAP dialer.
func NewDialer(version string, logger *slog.Logger) Dialer {
	return &imapDialer{version: version, logger: logger}
}

type imapDialer struct {
	version string
	logger  *slog.Logger
}

func (d *imapDialer) Dial(ctx context.Context, cfg config.Mail) (Conn, error) {
	c, err := connect(ctx, cfg, d.version, d.logger)
	if err != nil {
		return nil, err
	}
	data, err := c.Select(cfg.Mailbox, &imap.SelectOptions{ReadOnly: !cfg.MarkAsSeen}).Wait()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("select %s: %w", cfg.Mailbox, err)
	}
	return &imapConn{
		client:      c,
		logger:      d.logger,
		uidValidity: data.UIDValidity,
		uidNext:     uint32(data.UIDNext),
	}, nil
}

func connect(ctx context.Context, cfg config.Mail, version string, logger *slog.Logger) (*imapclient.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		MinVersion:         tls.VersionTLS12,
	}
	opts := &imapclient.Options{
		TLSConfig: tlsCfg,
		Dialer:    &net.Dialer{Timeout: 20 * time.Second},
	}

	var (
		c   *imapclient.Client
		err error
	)
	addr := cfg.Addr()
	switch cfg.Security {
	case "tls":
		c, err = imapclient.DialTLS(addr, opts)
	case "starttls":
		c, err = imapclient.DialStartTLS(addr, opts)
	case "none":
		c, err = imapclient.DialInsecure(addr, opts)
	default:
		return nil, fmt.Errorf("unsupported mail.security %q", cfg.Security)
	}
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}

	if err := c.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login %s: %w", cfg.Username, err)
	}

	if cfg.IsSendID() {
		if caps, capErr := c.Capability().Wait(); capErr != nil {
			logger.Warn("imap capability check failed", "error", capErr)
		} else if caps.Has(imap.CapID) {
			if _, idErr := c.ID(&imap.IDData{
				Name:    "SimpleNotify",
				Version: version,
				Vendor:  "SimpleNotify",
			}).Wait(); idErr != nil {
				logger.Warn("imap id command failed", "error", idErr)
			}
		}
	}
	return c, nil
}

type imapConn struct {
	client      *imapclient.Client
	logger      *slog.Logger
	uidValidity uint32
	uidNext     uint32
}

func (c *imapConn) UIDValidity() uint32 { return c.uidValidity }
func (c *imapConn) UIDNext() uint32     { return c.uidNext }

func (c *imapConn) SearchUnseen(ctx context.Context) ([]uint32, error) {
	return c.search(ctx, &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}})
}

func (c *imapConn) SearchAll(ctx context.Context) ([]uint32, error) {
	return c.search(ctx, &imap.SearchCriteria{})
}

func (c *imapConn) search(ctx context.Context, criteria *imap.SearchCriteria) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := c.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("uid search: %w", err)
	}
	uids := make([]uint32, 0, len(data.AllUIDs()))
	for _, uid := range data.AllUIDs() {
		uids = append(uids, uint32(uid))
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

func (c *imapConn) FetchMetas(ctx context.Context, uids []uint32) ([]Meta, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	set := imap.UIDSetNum(toIMAPUIDs(uids)...)
	bufs, err := c.client.Fetch(set, &imap.FetchOptions{
		UID:        true,
		RFC822Size: true,
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch metadata: %w", err)
	}
	metas := make([]Meta, 0, len(bufs))
	for _, buf := range bufs {
		metas = append(metas, Meta{UID: uint32(buf.UID), Size: buf.RFC822Size})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UID < metas[j].UID })
	return metas, nil
}

func (c *imapConn) FetchBodies(ctx context.Context, uids []uint32, peek bool) (map[uint32][]byte, error) {
	if len(uids) == 0 {
		return map[uint32][]byte{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	set := imap.UIDSetNum(toIMAPUIDs(uids)...)
	bufs, err := c.client.Fetch(set, &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: peek}},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch bodies: %w", err)
	}
	out := make(map[uint32][]byte, len(bufs))
	for _, buf := range bufs {
		uid := uint32(buf.UID)
		for _, section := range buf.BodySection {
			out[uid] = section.Bytes
			break
		}
	}
	return out, nil
}

func (c *imapConn) Close() error {
	if c.client == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.client.Logout().Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.logger.Warn("imap logout timed out")
	}
	return c.client.Close()
}

func toIMAPUIDs(uids []uint32) []imap.UID {
	out := make([]imap.UID, len(uids))
	for i, uid := range uids {
		out[i] = imap.UID(uid)
	}
	return out
}
