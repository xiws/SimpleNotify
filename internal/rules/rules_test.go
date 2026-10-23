package rules

import (
	"strings"
	"testing"

	"github.com/xiws/SimpleNotify/internal/config"
)

func rule(id, command string, match config.Match) config.Rule {
	return config.Rule{ID: id, Name: id, Match: match, Command: command}
}

func mustCompile(t *testing.T, rules ...config.Rule) *Engine {
	t.Helper()
	e, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return e
}

func TestMatchConditions(t *testing.T) {
	e := mustCompile(t,
		rule("from", "true", config.Match{From: "CI@Example.com"}),
		rule("subj", "true", config.Match{SubjectContains: "DEPLOY"}),
		rule("body", "true", config.Match{BodyContains: "hello world"}),
		rule("regex", "true", config.Match{SubjectRegex: `^\[alert\]`}),
		rule("and", "true", config.Match{SubjectContains: "release", BodyContains: "ok"}),
	)

	tests := []struct {
		name                string
		from, subject, body string
		wantID              string
	}{
		{"from case-insensitive", "ci@example.com", "x", "y", "from"},
		{"subject contains", "a", "please DEPLOY now", "y", "subj"},
		{"body contains", "a", "x", "say hello world!", "body"},
		{"regex", "a", "[alert] disk", "y", "regex"},
		{"and both", "a", "release 1.0", "all ok", "and"},
		{"and missing body", "a", "release 1.0", "nope", ""},
		{"no match", "a", "x", "y", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.Match(tt.from, tt.subject, tt.body)
			gotID := ""
			if got != nil {
				gotID = got.ID
			}
			if gotID != tt.wantID {
				t.Fatalf("got %q, want %q", gotID, tt.wantID)
			}
		})
	}
}

func TestFirstMatchWins(t *testing.T) {
	first := rule("first", "echo first", config.Match{SubjectContains: "x"})
	second := rule("second", "echo second", config.Match{SubjectContains: "x"})

	e := mustCompile(t, first, second)
	got := e.Match("a", "x", "b")
	if got == nil || got.ID != "first" {
		t.Fatalf("matched %+v", got)
	}
}

func TestDisabledRuleSkipped(t *testing.T) {
	disabled := false
	first := rule("first", "echo first", config.Match{SubjectContains: "x"})
	first.Enabled = &disabled
	second := rule("second", "echo second", config.Match{SubjectContains: "x"})

	e := mustCompile(t, first, second)
	got := e.Match("a", "x", "b")
	if got == nil || got.ID != "second" {
		t.Fatalf("matched %+v", got)
	}
}

func TestDisabledOnlyRule(t *testing.T) {
	disabled := false
	r := rule("only", "true", config.Match{SubjectContains: "x"})
	r.Enabled = &disabled
	e := mustCompile(t, r)
	if got := e.Match("a", "x", "b"); got != nil {
		t.Fatalf("matched %+v", got)
	}
}

func TestRender(t *testing.T) {
	e := mustCompile(t, rule("r", `echo "{{.Subject}} from {{.From}} ({{.RuleName}})"`, config.Match{SubjectContains: "x"}))
	r := e.Rules()[0]
	got, err := r.Render(Data{Subject: "hi", From: "a@b.c", RuleName: "r"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != `echo "hi from a@b.c (r)"` {
		t.Fatalf("got %q", got)
	}
}

func TestRenderUnknownField(t *testing.T) {
	e := mustCompile(t, rule("r", "echo {{.Nope}}", config.Match{SubjectContains: "x"}))
	if _, err := e.Rules()[0].Render(Data{}); err == nil {
		t.Fatal("expected template error")
	}
}

func TestCompileErrors(t *testing.T) {
	if _, err := Compile([]config.Rule{rule("bad", "echo {{", config.Match{SubjectContains: "x"})}); err == nil {
		t.Fatal("expected template parse error")
	}
	if _, err := Compile([]config.Rule{rule("badre", "true", config.Match{SubjectRegex: "("})}); err == nil {
		t.Fatal("expected regex error")
	}
}

func TestSummary(t *testing.T) {
	r := &Rule{
		fromSub:         "a@b",
		subjectContains: "x",
	}
	got := summarize(r)
	for _, want := range []string{"发件人", "a@b", "主题", "x"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}
