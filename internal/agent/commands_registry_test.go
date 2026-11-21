package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// errorProvider returns a model-list error, which /model must treat as a
// validation failure rather than a licence to set the model.
type errorProvider struct{ name string }

func (p *errorProvider) Name() string { return p.name }
func (p *errorProvider) Models(context.Context) ([]string, error) {
	return nil, errors.New("catalog offline")
}
func (p *errorProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("not used")
}

// TestCommandRegistryShape pins the registry contract the palette, completion
// and /help all depend on: 13 unique commands, each with metadata and a
// handler, and no duplicate names or aliases.
func TestCommandRegistryShape(t *testing.T) {
	specs := Commands()
	want := []string{
		"/help", "/clear", "/compact", "/stats", "/new", "/sessions",
		"/resume", "/commit", "/providers", "/provider", "/model",
		"/exit", "/quit",
	}
	if len(specs) != len(want) {
		t.Fatalf("registry has %d commands, want %d", len(specs), len(want))
	}
	byName := make(map[string]CommandSpec, len(specs))
	seen := make(map[string]bool)
	for _, spec := range specs {
		if spec.Name == "" || !strings.HasPrefix(spec.Name, "/") {
			t.Errorf("command %q is not slash-prefixed", spec.Name)
		}
		if spec.Description == "" || spec.Usage == "" || spec.Handle == nil {
			t.Errorf("%s is missing description, usage or handler", spec.Name)
		}
		if seen[strings.ToLower(spec.Name)] {
			t.Errorf("duplicate command name %q", spec.Name)
		}
		seen[strings.ToLower(spec.Name)] = true
		for _, alias := range spec.Aliases {
			if seen[strings.ToLower(alias)] {
				t.Errorf("alias %q of %s collides with another command", alias, spec.Name)
			}
			seen[strings.ToLower(alias)] = true
		}
		byName[spec.Name] = spec
	}
	for _, name := range want {
		if _, ok := byName[name]; !ok {
			t.Errorf("registry is missing %s", name)
		}
	}
}

// TestHelpIsGeneratedFromRegistry is the drift guard: every command appears
// exactly once, with its generated usage and description, and no extra command
// rows exist that the registry does not know about.
func TestHelpIsGeneratedFromRegistry(t *testing.T) {
	reg := provider.NewRegistry()
	ctx := CommandContext{Session: NewSession("m1"), Registry: reg}

	res := HandleCommand(context.Background(), ctx, "/help")
	if !res.Handled || res.Error != nil {
		t.Fatalf("help failed: handled=%v err=%v", res.Handled, res.Error)
	}
	if !strings.HasPrefix(res.Output, "Available commands:\n") {
		t.Fatalf("help does not start with the generated header: %q", res.Output)
	}

	lines := strings.Split(res.Output, "\n")
	var rows []string
	for _, line := range lines {
		if strings.HasPrefix(line, "  /") {
			rows = append(rows, line)
		}
	}
	specs := Commands()
	if len(rows) != len(specs) {
		t.Fatalf("help has %d command rows, registry has %d:\n%s", len(rows), len(specs), res.Output)
	}
	for i, spec := range specs {
		row := rows[i]
		if !strings.HasPrefix(row, "  "+spec.Usage) {
			t.Errorf("row %d does not start with usage %q: %q", i, spec.Usage, row)
		}
		if !strings.Contains(row, "— "+spec.Description) {
			t.Errorf("row %d does not carry the description %q: %q", i, spec.Description, row)
		}
	}
	if !strings.Contains(res.Output, "Ctrl+K") {
		t.Errorf("help does not mention the command palette: %q", res.Output)
	}
}

// TestCommandDispatchEquivalence walks every registered command, plus the bare
// aliases that predate the registry, and asserts the same handled/exit/output
// contract the switch used to produce.
func TestCommandDispatchEquivalence(t *testing.T) {
	newCtx := func() CommandContext {
		reg := provider.NewRegistry()
		reg.Register(&mockProvider{name: "provA", models: []string{"model-1", "model-2"}})
		reg.Register(&mockProvider{name: "provB", models: []string{"model-3"}})
		sess := NewSession("model-1")
		sess.AddMessage(provider.Message{Role: "user", Content: "hello"})
		return CommandContext{Session: sess, Registry: reg, ContextManager: NewContextManager(8192)}
	}

	tests := []struct {
		name      string
		input     string
		handled   bool
		exit      bool
		outputHas []string
		errorHas  []string
		check     func(t *testing.T, ctx CommandContext)
	}{
		{
			name: "help", input: "/help", handled: true,
			outputHas: []string{"Available commands", "/model [name]"},
		},
		{
			name: "help case insensitive", input: "/HELP", handled: true,
			outputHas: []string{"Available commands"},
		},
		{
			name: "clear", input: "/clear", handled: true,
			outputHas: []string{"Session history cleared."},
			check: func(t *testing.T, ctx CommandContext) {
				hist := ctx.Session.History()
				if len(hist) != 1 || hist[0].Role != "system" {
					t.Errorf("clear left %d messages, want one system prompt", len(hist))
				}
			},
		},
		{
			name: "compact", input: "/compact", handled: true,
			outputHas: []string{"Compacted history"},
		},
		{
			name: "stats", input: "/stats", handled: true,
			outputHas: []string{"tokens"},
		},
		{
			name: "new", input: "/new", handled: true,
			outputHas: []string{"Started new session"},
		},
		{
			name: "sessions without store", input: "/sessions", handled: true,
			outputHas: []string{"Session store is not enabled."},
		},
		{
			name: "sessions with arg routes to resume", input: "/sessions abc", handled: true,
			outputHas: []string{"Session store is not enabled."},
		},
		{
			name: "resume usage", input: "/resume", handled: true,
			outputHas: []string{"Usage: /resume <session-id>"},
		},
		{
			name: "commit without files", input: "/commit", handled: true,
			outputHas: []string{"No modified files tracked in this session."},
		},
		{
			name: "commit with message", input: "/commit some message", handled: true,
			outputHas: []string{"No modified files tracked in this session."},
		},
		{
			name: "providers", input: "/providers", handled: true,
			outputHas: []string{"Active Provider: provA", "provB", "model-1"},
		},
		{
			name: "provider no arg", input: "/provider", handled: true,
			outputHas: []string{"Active Provider: provA", "use '/provider <name>' to switch"},
		},
		{
			name: "provider switch", input: "/provider provB", handled: true,
			outputHas: []string{"Switched provider to provB."},
			check: func(t *testing.T, ctx CommandContext) {
				if got := ctx.Registry.ActiveName(); got != "provB" {
					t.Errorf("active provider = %q, want provB", got)
				}
				if got := ctx.Session.Provider(); got != "provB" {
					t.Errorf("session provider = %q, want provB", got)
				}
			},
		},
		{
			name: "model show", input: "/model", handled: true,
			outputHas: []string{"Active Model: model-1", "model-2"},
		},
		{
			name: "model set", input: "/model model-2", handled: true,
			outputHas: []string{"Model set to model-2."},
			check: func(t *testing.T, ctx CommandContext) {
				if got := ctx.Session.Model(); got != "model-2" {
					t.Errorf("session model = %q, want model-2", got)
				}
			},
		},
		{
			name: "exit", input: "/exit", handled: true, exit: true,
			outputHas: []string{"Goodbye!"},
		},
		{
			name: "quit", input: "/quit", handled: true, exit: true,
			outputHas: []string{"Goodbye!"},
		},
		{
			name: "bare exit", input: "exit", handled: true, exit: true,
			outputHas: []string{"Goodbye!"},
		},
		{
			name: "bare quit", input: "quit", handled: true, exit: true,
			outputHas: []string{"Goodbye!"},
		},
		{
			name: "bare exit mixed case", input: "EXIT", handled: true, exit: true,
			outputHas: []string{"Goodbye!"},
		},
		{
			name: "bare alias with words is a prompt", input: "exit now", handled: false,
		},
		{
			name: "unknown command", input: "/bogus", handled: true,
			errorHas: []string{`unknown command "/bogus" — type /help to see available commands`},
		},
		{
			name: "plain prompt is not a command", input: "hello world", handled: false,
		},
		{
			name: "bare word is not a command", input: "help", handled: false,
		},
		{
			name: "empty input", input: "   ", handled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newCtx()
			res := HandleCommand(context.Background(), ctx, tc.input)
			if res.Handled != tc.handled {
				t.Fatalf("Handled = %v, want %v (output %q, err %v)", res.Handled, tc.handled, res.Output, res.Error)
			}
			if res.Exit != tc.exit {
				t.Errorf("Exit = %v, want %v", res.Exit, tc.exit)
			}
			for _, want := range tc.outputHas {
				if !strings.Contains(res.Output, want) {
					t.Errorf("output missing %q: %q", want, res.Output)
				}
			}
			for _, want := range tc.errorHas {
				if res.Error == nil {
					t.Fatalf("expected error containing %q, got nil", want)
				}
				if !strings.Contains(res.Error.Error(), want) {
					t.Errorf("error %q missing %q", res.Error, want)
				}
			}
			if len(tc.errorHas) == 0 && res.Error != nil && !tc.exit {
				t.Errorf("unexpected error: %v", res.Error)
			}
			if tc.check != nil {
				tc.check(t, ctx)
			}
		})
	}
}

// TestModelCommandFailsClosed pins every refusal path: an unset registry, an
// unavailable provider, an erroring catalog, an empty catalog, and a model the
// catalog does not list. None may change the session model.
func TestModelCommandFailsClosed(t *testing.T) {
	sess := NewSession("original")
	base := func(reg *provider.Registry) CommandContext {
		return CommandContext{Session: sess, Registry: reg}
	}

	t.Run("no registry", func(t *testing.T) {
		res := HandleCommand(context.Background(), base(nil), "/model anything")
		if res.Error == nil || !strings.Contains(res.Error.Error(), "no provider registry configured") {
			t.Fatalf("expected registry error, got %v", res.Error)
		}
	})

	t.Run("no active provider", func(t *testing.T) {
		res := HandleCommand(context.Background(), base(provider.NewRegistry()), "/model anything")
		if res.Error == nil || !strings.Contains(res.Error.Error(), "active provider unavailable") {
			t.Fatalf("expected active-provider error, got %v", res.Error)
		}
	})

	t.Run("model list error", func(t *testing.T) {
		reg := provider.NewRegistry()
		reg.Register(&errorProvider{name: "broken"})
		res := HandleCommand(context.Background(), base(reg), "/model anything")
		if res.Error == nil || !strings.Contains(res.Error.Error(), "model list unavailable") {
			t.Fatalf("expected model-list error, got %v", res.Error)
		}
	})

	t.Run("empty model list", func(t *testing.T) {
		reg := provider.NewRegistry()
		reg.Register(&mockProvider{name: "empty"})
		res := HandleCommand(context.Background(), base(reg), "/model anything")
		if res.Error == nil || !strings.Contains(res.Error.Error(), "empty model list") {
			t.Fatalf("expected empty-list error, got %v", res.Error)
		}
	})

	t.Run("model not listed", func(t *testing.T) {
		reg := provider.NewRegistry()
		reg.Register(&mockProvider{name: "full", models: []string{"known"}})
		res := HandleCommand(context.Background(), base(reg), "/model unknown")
		if res.Error == nil || !strings.Contains(res.Error.Error(), `model "unknown" is not available on full — available: known`) {
			t.Fatalf("expected unavailable-model error, got %v", res.Error)
		}
	})

	if got := sess.Model(); got != "original" {
		t.Fatalf("a failed validation changed the model to %q", got)
	}
}

// TestCommitApprovalPromptUnchanged guards the security path: /commit must
// still show every file, the full message with trailer, and the exact prompt,
// and must do nothing when the user declines.
func TestCommitApprovalPromptUnchanged(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession("test-model")
	for _, name := range []string{"one.txt", "two.txt"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("change"), 0o644); err != nil {
			t.Fatal(err)
		}
		sess.TrackModifiedFile(path)
	}

	var prompt string
	res := HandleCommand(context.Background(), CommandContext{
		Session:       sess,
		WorkspaceRoot: dir,
		AskApproval: func(p string) bool {
			prompt = p
			return false
		},
	}, "/commit fix the bug")
	if !res.Handled || !strings.Contains(res.Output, "Commit cancelled by user.") {
		t.Fatalf("declined commit output = %q", res.Output)
	}
	for _, want := range []string{
		"one.txt", "two.txt",
		"fix the bug",
		"Assisted-by: GoCode:test-model",
		"Approve this commit? [y/N]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("approval prompt missing %q: %q", want, prompt)
		}
	}
}

// TestCommandsCopyIsolation ensures callers cannot mutate the registry through
// the slice Commands returns (the palette holds it for the process lifetime).
func TestCommandsCopyIsolation(t *testing.T) {
	specs := Commands()
	specs[0].Name = "/mutated"
	if got := Commands()[0].Name; got != "/help" {
		t.Fatalf("registry mutated through Commands(): first command now %q", got)
	}
}
