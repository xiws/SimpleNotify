// Package rules compiles and evaluates email matching rules.
package rules

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/xiws/SimpleNotify/internal/config"
)

// Data is the template data available to rule commands.
type Data struct {
	From      string
	To        string
	Subject   string
	Date      string
	MessageID string
	UID       string
	Mailbox   string
	Body      string
	RuleID    string
	RuleName  string
}

// Rule is a compiled rule.
type Rule struct {
	ID      string
	Name    string
	Enabled bool
	Match   config.Match
	Command string

	tmpl            *template.Template
	fromSub         string
	subjectContains string
	bodyContains    string
	subjectRegex    *regexp.Regexp
	bodyRegex       *regexp.Regexp
	summary         string
}

// Summary returns a human readable description of the match conditions.
func (r *Rule) Summary() string { return r.summary }

// Render executes the command template with data.
func (r *Rule) Render(data Data) (string, error) {
	var sb strings.Builder
	if err := r.tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("render command template: %w", err)
	}
	return sb.String(), nil
}

// Matches reports whether the message satisfies every configured condition.
func (r *Rule) Matches(from, subject, body string) bool {
	if r.fromSub != "" && !containsFold(from, r.fromSub) {
		return false
	}
	if r.subjectContains != "" && !containsFold(subject, r.subjectContains) {
		return false
	}
	if r.subjectRegex != nil && !r.subjectRegex.MatchString(subject) {
		return false
	}
	if r.bodyContains != "" && !containsFold(body, r.bodyContains) {
		return false
	}
	if r.bodyRegex != nil && !r.bodyRegex.MatchString(body) {
		return false
	}
	return true
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// Engine evaluates rules in configuration order.
type Engine struct {
	rules []*Rule
}

// Compile builds an immutable rule engine.
func Compile(cfg []config.Rule) (*Engine, error) {
	e := &Engine{rules: make([]*Rule, 0, len(cfg))}
	for _, rc := range cfg {
		r, err := compileRule(rc)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rc.ID, err)
		}
		e.rules = append(e.rules, r)
	}
	return e, nil
}

func compileRule(rc config.Rule) (*Rule, error) {
	r := &Rule{
		ID:              rc.ID,
		Name:            rc.Name,
		Enabled:         rc.IsEnabled(),
		Match:           rc.Match,
		Command:         rc.Command,
		fromSub:         strings.TrimSpace(rc.Match.From),
		subjectContains: rc.Match.SubjectContains,
		bodyContains:    rc.Match.BodyContains,
	}
	// trim only the from condition; contains conditions keep user whitespace
	r.Match.From = r.fromSub

	tmpl, err := template.New(rc.ID).Parse(rc.Command)
	if err != nil {
		return nil, fmt.Errorf("parse command template: %w", err)
	}
	r.tmpl = tmpl

	if rc.Match.SubjectRegex != "" {
		re, err := regexp.Compile(rc.Match.SubjectRegex)
		if err != nil {
			return nil, fmt.Errorf("subject_regex: %w", err)
		}
		r.subjectRegex = re
	}
	if rc.Match.BodyRegex != "" {
		re, err := regexp.Compile(rc.Match.BodyRegex)
		if err != nil {
			return nil, fmt.Errorf("body_regex: %w", err)
		}
		r.bodyRegex = re
	}
	r.summary = summarize(r)
	return r, nil
}

// Match returns the first enabled rule that matches, or nil.
func (e *Engine) Match(from, subject, body string) *Rule {
	if e == nil {
		return nil
	}
	for _, r := range e.rules {
		if !r.Enabled {
			continue
		}
		if r.Matches(from, subject, body) {
			return r
		}
	}
	return nil
}

// Rules returns the compiled rules in configuration order.
func (e *Engine) Rules() []*Rule {
	if e == nil {
		return nil
	}
	out := make([]*Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

func summarize(r *Rule) string {
	var parts []string
	if r.fromSub != "" {
		parts = append(parts, fmt.Sprintf("发件人包含 %q", r.fromSub))
	}
	if r.subjectContains != "" {
		parts = append(parts, fmt.Sprintf("主题包含 %q", r.subjectContains))
	}
	if r.subjectRegex != nil {
		parts = append(parts, fmt.Sprintf("主题匹配 /%s/", r.subjectRegex.String()))
	}
	if r.bodyContains != "" {
		parts = append(parts, fmt.Sprintf("正文包含 %q", r.bodyContains))
	}
	if r.bodyRegex != nil {
		parts = append(parts, fmt.Sprintf("正文匹配 /%s/", r.bodyRegex.String()))
	}
	if len(parts) == 0 {
		return "无匹配条件"
	}
	return strings.Join(parts, "；")
}
