// Package execute runs rule commands with a timeout and output limits.
package execute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiws/SimpleNotify/internal/config"
	"github.com/xiws/SimpleNotify/internal/mail"
	"github.com/xiws/SimpleNotify/internal/rules"
)

// Result is the outcome of one command execution.
type Result struct {
	Command    string
	Status     string
	ExitCode   int
	Stdout     string
	Stderr     string
	Error      string
	Truncated  bool
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// Runner executes rendered commands.
type Runner struct {
	shell     string
	timeout   time.Duration
	workDir   string
	maxOutput int64
	env       map[string]string
	logger    *slog.Logger
}

// New creates a Runner and verifies that the configured shell exists.
func New(cfg config.Execute, logger *slog.Logger) (*Runner, error) {
	if _, err := exec.LookPath(cfg.Shell); err != nil {
		return nil, fmt.Errorf("execute.shell %q not found: %w", cfg.Shell, err)
	}
	env := make(map[string]string, len(cfg.Env))
	for k, v := range cfg.Env {
		env[k] = v
	}
	return &Runner{
		shell:     cfg.Shell,
		timeout:   cfg.Timeout.Duration(),
		workDir:   cfg.WorkDir,
		maxOutput: cfg.MaxOutputBytes,
		env:       env,
		logger:    logger,
	}, nil
}

// Run renders and executes the rule command for msg.
func (r *Runner) Run(ctx context.Context, rule *rules.Rule, msg mail.Message) Result {
	started := time.Now()
	data := rules.Data{
		From:      msg.From,
		To:        msg.To,
		Subject:   msg.Subject,
		Date:      msg.Date.Format(time.RFC3339),
		MessageID: msg.MessageID,
		UID:       strconv.FormatUint(uint64(msg.UID), 10),
		Mailbox:   msg.Mailbox,
		Body:      msg.Body,
		RuleID:    rule.ID,
		RuleName:  rule.Name,
	}

	command, err := rule.Render(data)
	if err != nil {
		finished := time.Now()
		return Result{
			Status:     "error",
			ExitCode:   -1,
			Error:      err.Error(),
			StartedAt:  started,
			FinishedAt: finished,
			Duration:   finished.Sub(started),
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.shell, "-c", command)
	cmd.Dir = r.workDir
	cmd.Env = r.buildEnv(data)
	stdout := &limitedBuffer{max: r.maxOutput}
	stderr := &limitedBuffer{max: r.maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	finished := time.Now()

	res := Result{
		Command:    command,
		ExitCode:   0,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Truncated:  stdout.truncated || stderr.truncated,
		StartedAt:  started,
		FinishedAt: finished,
		Duration:   finished.Sub(started),
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.Status = "timeout"
		res.ExitCode = -1
		res.Error = fmt.Sprintf("command timed out after %s", r.timeout)
	case ctx.Err() != nil:
		res.Status = "error"
		res.ExitCode = -1
		res.Error = "command cancelled"
	case runErr == nil:
		res.Status = "success"
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.Status = "failed"
			res.ExitCode = exitErr.ExitCode()
			if res.ExitCode == -1 {
				res.Error = runErr.Error()
			}
		} else {
			res.Status = "error"
			res.ExitCode = -1
			res.Error = runErr.Error()
		}
	}
	return res
}

func (r *Runner) buildEnv(data rules.Data) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(r.env)+8)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		// Drop inherited variables we are about to define so the child always
		// sees our values, not the parent's.
		if strings.HasPrefix(key, "SN_") {
			continue
		}
		if _, ok := r.env[key]; ok {
			continue
		}
		env = append(env, kv)
	}

	keys := make([]string, 0, len(r.env))
	for k := range r.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+r.env[k])
	}

	env = append(env,
		"SN_FROM="+data.From,
		"SN_TO="+data.To,
		"SN_SUBJECT="+data.Subject,
		"SN_DATE="+data.Date,
		"SN_MESSAGE_ID="+data.MessageID,
		"SN_UID="+data.UID,
		"SN_MAILBOX="+data.Mailbox,
		"SN_BODY="+data.Body,
		"SN_RULE_ID="+data.RuleID,
		"SN_RULE_NAME="+data.RuleName,
	)
	return env
}

type limitedBuffer struct {
	max       int64
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
	} else {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
