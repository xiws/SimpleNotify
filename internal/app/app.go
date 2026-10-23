// Package app wires configuration, mail polling, command execution and the
// admin web server together.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/execute"
	"github.com/xiws/SimpleNotify/internal/history"
	"github.com/xiws/SimpleNotify/internal/mail"
	"github.com/xiws/SimpleNotify/internal/rules"
	"github.com/xiws/SimpleNotify/internal/state"
	"github.com/xiws/SimpleNotify/internal/web"
)

// App is the running SimpleNotify server.
type App struct {
	version   string
	cfgPath   string
	startedAt time.Time

	cfg    atomic.Pointer[config.Config]
	engine atomic.Pointer[rules.Engine]
	runner atomic.Pointer[execute.Runner]

	logger  *slog.Logger
	level   *slog.LevelVar
	logFile *os.File

	state   *state.Store
	history *history.Store

	poller       *mail.Poller
	pollerCancel context.CancelFunc
	pollerWG     sync.WaitGroup
	pollerMu     sync.Mutex

	baseCtx    context.Context
	baseCancel context.CancelFunc

	httpServer *http.Server
	addr       string
}

// Run loads the configuration, starts the poller and admin server, and blocks
// until the process receives SIGINT/SIGTERM, ctx is cancelled, or the HTTP
// server fails. SIGHUP reloads the configuration.
func Run(ctx context.Context, version, cfgPath, addrOverride string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	a, err := newApp(version, cfgPath, cfg)
	if err != nil {
		return err
	}

	a.baseCtx, a.baseCancel = context.WithCancel(context.Background())
	defer a.close()

	addr := cfg.Server.Addr
	if addrOverride != "" {
		if err := cfg.ValidateListenAddr(addrOverride); err != nil {
			return fmt.Errorf("--addr: %w", err)
		}
		addr = addrOverride
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	a.addr = ln.Addr().String()

	a.httpServer = &http.Server{
		Handler: web.New(web.Options{
			Version:  version,
			Password: func() string { return a.cfg.Load().Server.Password },
			Snapshot: a.Snapshot,
			List:     a.history.List,
			Get:      a.history.Get,
			Logger:   a.logger,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := a.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	a.startPoller()
	a.logger.Info("SimpleNotify started",
		"version", version,
		"config", cfg.Path,
		"admin", "http://"+a.addr+"/",
		"mail_enabled", cfg.Mail.IsEnabled(),
		"rules", len(cfg.Rules),
	)

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	for {
		select {
		case <-sigCtx.Done():
			a.logger.Info("shutting down")
			return nil
		case <-hup:
			a.reload()
		case err := <-serveErr:
			return fmt.Errorf("http server: %w", err)
		}
	}
}

func newApp(version, cfgPath string, cfg *config.Config) (*App, error) {
	level := &slog.LevelVar{}
	level.Set(parseLevel(cfg.Log.Level))

	writer := io.Writer(os.Stderr)
	var logFile *os.File
	if cfg.Log.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.Log.File), 0o700); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open log file %s: %w", cfg.Log.File, err)
		}
		logFile = f
		writer = io.MultiWriter(os.Stderr, f)
	}
	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level}))

	st, err := state.Open(cfg.State.File)
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	hist, err := history.Open(cfg.History.Memory, cfg.History.File, logger)
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	engine, err := rules.Compile(cfg.Rules)
	if err != nil {
		hist.Close()
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	runner, err := execute.New(cfg.Execute, logger)
	if err != nil {
		hist.Close()
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	a := &App{
		version:   version,
		cfgPath:   cfgPath,
		startedAt: time.Now(),
		logger:    logger,
		level:     level,
		logFile:   logFile,
		state:     st,
		history:   hist,
	}
	a.cfg.Store(cfg)
	a.engine.Store(engine)
	a.runner.Store(runner)
	return a, nil
}

func (a *App) close() {
	if a.baseCancel != nil {
		a.baseCancel()
	}
	a.stopPoller()

	if a.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = a.httpServer.Shutdown(shutdownCtx)
		cancel()
	}
	if err := a.history.Close(); err != nil {
		a.logger.Warn("close history failed", "error", err)
	}
	if a.logFile != nil {
		a.logFile.Close()
	}
}

func (a *App) startPoller() {
	a.pollerMu.Lock()
	defer a.pollerMu.Unlock()
	if a.pollerCancel != nil {
		return
	}
	cfg := a.cfg.Load()
	p := mail.New(
		mail.NewDialer(a.version, a.logger),
		a.state,
		a,
		func() config.Mail { return a.cfg.Load().Mail },
		a.logger,
	)
	a.poller = p
	ctx, cancel := context.WithCancel(a.baseCtx)
	a.pollerCancel = cancel
	a.pollerWG.Add(1)
	go func() {
		defer a.pollerWG.Done()
		p.Run(ctx)
	}()
	a.logger.Debug("poller started", "mail_enabled", cfg.Mail.IsEnabled())
}

func (a *App) stopPoller() {
	a.pollerMu.Lock()
	cancel := a.pollerCancel
	a.pollerCancel = nil
	a.pollerMu.Unlock()
	if cancel == nil {
		return
	}

	cancel()
	done := make(chan struct{})
	go func() {
		a.pollerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		a.logger.Warn("poller did not stop in time")
	}
}

func (a *App) reload() {
	a.logger.Info("reloading configuration", "path", a.cfgPath)
	newCfg, err := config.Load(a.cfgPath)
	if err != nil {
		a.logger.Error("reload failed, keeping previous configuration", "error", err)
		return
	}

	engine, err := rules.Compile(newCfg.Rules)
	if err != nil {
		a.logger.Error("reload failed, invalid rules", "error", err)
		return
	}
	runner, err := execute.New(newCfg.Execute, a.logger)
	if err != nil {
		a.logger.Error("reload failed, invalid execute config", "error", err)
		return
	}

	old := a.cfg.Load()
	if old.Server.Addr != newCfg.Server.Addr {
		a.logger.Warn("server.addr changes require a restart", "old", old.Server.Addr, "new", newCfg.Server.Addr)
	}
	if old.State.File != newCfg.State.File || old.History.File != newCfg.History.File || old.History.Memory != newCfg.History.Memory {
		a.logger.Warn("state/history path changes require a restart")
	}
	if old.Log.File != newCfg.Log.File {
		a.logger.Warn("log.file changes require a restart", "old", old.Log.File, "new", newCfg.Log.File)
	}

	a.level.Set(parseLevel(newCfg.Log.Level))
	a.cfg.Store(newCfg)
	a.engine.Store(engine)
	a.runner.Store(runner)

	if !mailSettingsEqual(old.Mail, newCfg.Mail) {
		a.stopPoller()
		a.startPoller()
	}

	a.logger.Info("configuration reloaded", "rules", len(newCfg.Rules), "mail_enabled", newCfg.Mail.IsEnabled())
}

// Process implements mail.Processor.
func (a *App) Process(ctx context.Context, msg mail.Message) (mail.ProcessResult, error) {
	rule := a.engine.Load().Match(msg.From, msg.Subject, msg.Body)
	if rule == nil {
		a.logger.Debug("no rule matched", "uid", msg.UID, "subject", msg.Subject)
		return mail.ProcessResult{}, nil
	}

	result := a.runner.Load().Run(ctx, rule, msg)
	entry := history.Entry{
		ID:         history.NewID(result.StartedAt),
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
		DurationMS: result.Duration.Milliseconds(),
		RuleID:     rule.ID,
		RuleName:   rule.Name,
		UID:        msg.UID,
		Mailbox:    msg.Mailbox,
		From:       msg.From,
		Subject:    msg.Subject,
		MessageID:  msg.MessageID,
		Command:    result.Command,
		Status:     result.Status,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		Error:      result.Error,
		Truncated:  result.Truncated,
	}
	a.history.Add(entry)

	if result.Status == "success" {
		a.logger.Info("rule executed",
			"rule", rule.ID, "uid", msg.UID, "duration_ms", entry.DurationMS)
	} else {
		a.logger.Warn("rule execution failed",
			"rule", rule.ID, "uid", msg.UID, "status", result.Status,
			"exit_code", result.ExitCode, "error", result.Error)
	}
	return mail.ProcessResult{Matched: true, Status: result.Status}, nil
}

// Snapshot implements the web status provider.
func (a *App) Snapshot() web.Snapshot {
	cfg := a.cfg.Load()

	a.pollerMu.Lock()
	poller := a.poller
	a.pollerMu.Unlock()

	var ms mail.Status
	if poller != nil {
		ms = poller.Status()
	} else {
		ms = mail.Status{
			Enabled:      cfg.Mail.IsEnabled(),
			Host:         cfg.Mail.Addr(),
			Username:     cfg.Mail.Username,
			Mailbox:      cfg.Mail.Mailbox,
			PollInterval: cfg.Mail.PollInterval.Duration(),
			OnStart:      cfg.Mail.OnStart,
			MarkAsSeen:   cfg.Mail.MarkAsSeen,
		}
	}

	latest := a.history.LatestByRule()
	compiled := a.engine.Load().Rules()
	ruleInfos := make([]web.RuleInfo, 0, len(compiled))
	for _, r := range compiled {
		info := web.RuleInfo{
			ID:      r.ID,
			Name:    r.Name,
			Enabled: r.Enabled,
			Summary: r.Summary(),
			Command: r.Command,
		}
		if e, ok := latest[r.ID]; ok {
			started := e.StartedAt
			exitCode := e.ExitCode
			info.LastRunAt = &started
			info.LastStatus = e.Status
			info.LastExitCode = &exitCode
		}
		ruleInfos = append(ruleInfos, info)
	}

	return web.Snapshot{
		Version:    a.version,
		StartedAt:  a.startedAt,
		ConfigPath: cfg.Path,
		Addr:       a.addrOrConfig(cfg),
		Mail: web.MailInfo{
			Enabled:         cfg.Mail.IsEnabled(),
			Host:            cfg.Mail.Addr(),
			Username:        config.MaskUsername(cfg.Mail.Username),
			Mailbox:         cfg.Mail.Mailbox,
			PollInterval:    cfg.Mail.PollInterval.String(),
			OnStart:         cfg.Mail.OnStart,
			MarkAsSeen:      cfg.Mail.MarkAsSeen,
			SendID:          cfg.Mail.IsSendID(),
			MaxMessageBytes: cfg.Mail.MaxMessageBytes,
			MaxBodyBytes:    cfg.Mail.MaxBodyBytes,
			Connected:       ms.Connected,
			LastPollAt:      timePtr(ms.LastPollAt),
			LastSuccessAt:   timePtr(ms.LastSuccessAt),
			NextPollAt:      timePtr(ms.NextPollAt),
			LastError:       ms.LastError,
			UIDValidity:     ms.UIDValidity,
			LastUID:         ms.LastUID,
		},
		Execute: web.ExecuteInfo{
			Shell:   cfg.Execute.Shell,
			Timeout: cfg.Execute.Timeout.String(),
			WorkDir: cfg.Execute.WorkDir,
		},
		Counters: web.Counters{
			Polls:     ms.Counters.Polls,
			Messages:  ms.Counters.Messages,
			Matched:   ms.Counters.Matched,
			Succeeded: ms.Counters.Succeeded,
			Failed:    ms.Counters.Failed,
			Skipped:   ms.Counters.Skipped,
		},
		Rules: ruleInfos,
	}
}

func (a *App) addrOrConfig(cfg *config.Config) string {
	if a.addr != "" {
		return a.addr
	}
	return cfg.Server.Addr
}

func parseLevel(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func mailSettingsEqual(a, b config.Mail) bool {
	return a.IsEnabled() == b.IsEnabled() &&
		a.Host == b.Host &&
		a.Port == b.Port &&
		a.Security == b.Security &&
		a.InsecureSkipVerify == b.InsecureSkipVerify &&
		a.Username == b.Username &&
		a.Password == b.Password &&
		a.Mailbox == b.Mailbox &&
		a.PollInterval == b.PollInterval &&
		a.OnStart == b.OnStart &&
		a.MarkAsSeen == b.MarkAsSeen &&
		a.IsSendID() == b.IsSendID() &&
		a.MaxMessageBytes == b.MaxMessageBytes &&
		a.MaxBodyBytes == b.MaxBodyBytes
}
