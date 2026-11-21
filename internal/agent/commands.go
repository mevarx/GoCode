package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/hooks"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
	"github.com/mevarx/GoCode/internal/tools"
)

// CommandContext holds the environment and services needed to execute slash commands.
type CommandContext struct {
	Session        *Session
	Registry       *provider.Registry
	ContextManager *ContextManager
	WorkspaceRoot  string
	AskApproval    func(prompt string) bool
	// Hooks, when set, runs the hook engine for commands that compact
	// (PreCompact deny blocks /compact too). Nil means no hooks.
	Hooks *hooks.Runner
	// Plan, when set, is restated in the compaction summary prompt.
	Plan *tools.PlanStore

	// Ctx is the caller's context, installed by HandleCommand before the
	// handler runs. Handlers that are invoked directly (tests, the palette)
	// read it through Context, which never returns nil.
	Ctx context.Context
}

// Context returns the caller's context, or a background one when a handler is
// invoked without HandleCommand.
func (c CommandContext) Context() context.Context {
	if c.Ctx != nil {
		return c.Ctx
	}
	return context.Background()
}

// CommandResult represents the outcome of executing a command.
type CommandResult struct {
	Handled bool
	Output  string
	Error   error
	Exit    bool
}

// CommandSpec is one entry in the slash-command registry: the metadata the
// help text, palette and completion read, and the handler dispatch runs.
//
// Name and Aliases carry their leading slash; aliases without a slash (exit,
// quit) are the bare forms accepted for compatibility.
type CommandSpec struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Handle      func(CommandContext, string) CommandResult
}

// commandRegistry is the single source of truth for slash commands. Adding a
// command means adding a spec: dispatch, /help, completion and the palette all
// read this table.
//
// It is populated in init because /help renders from the table: a package-level
// initializer would make commandRegistry depend on its own handlers.
var commandRegistry []CommandSpec

func init() {
	commandRegistry = []CommandSpec{
		{
			Name:        "/help",
			Description: "Show this help message",
			Usage:       "/help",
			Handle:      handleHelp,
		},
		{
			Name:        "/clear",
			Description: "Clear conversation history and reset context",
			Usage:       "/clear",
			Handle:      handleClear,
		},
		{
			Name:        "/compact",
			Description: "Summarize earlier turns and compact history",
			Usage:       "/compact",
			Handle:      handleCompact,
		},
		{
			Name:        "/stats",
			Description: "Show current token usage and message count",
			Usage:       "/stats",
			Handle:      handleStats,
		},
		{
			Name:        "/new",
			Description: "Start a new session",
			Usage:       "/new",
			Handle:      handleNew,
		},
		{
			Name:        "/sessions",
			Description: "List previous saved sessions, or resume one by ID",
			Usage:       "/sessions [id]",
			Handle:      handleSessions,
		},
		{
			Name:        "/resume",
			Description: "Resume session by ID",
			Usage:       "/resume <id>",
			Handle:      handleResume,
		},
		{
			Name:        "/commit",
			Description: "Commit modified files with attribution trailer",
			Usage:       "/commit [msg]",
			Handle:      handleCommit,
		},
		{
			Name:        "/providers",
			Description: "List all available providers and their models",
			Usage:       "/providers",
			Handle:      handleProviders,
		},
		{
			Name:        "/provider",
			Description: "Switch active provider",
			Usage:       "/provider <name>",
			Handle:      handleProvider,
		},
		{
			Name:        "/model",
			Description: "Show current model, or switch model",
			Usage:       "/model [name]",
			Handle:      handleModel,
		},
		{
			Name:        "/exit",
			Aliases:     []string{"exit"},
			Description: "Quit GoCode",
			Usage:       "/exit",
			Handle:      handleExit,
		},
		{
			Name:        "/quit",
			Aliases:     []string{"quit"},
			Description: "Quit GoCode",
			Usage:       "/quit",
			Handle:      handleExit,
		},
	}
}

// Commands returns a copy of the registry in display order. The TUI palette,
// completion and help rendering read it so command metadata has one source.
func Commands() []CommandSpec {
	out := make([]CommandSpec, len(commandRegistry))
	copy(out, commandRegistry)
	// Deep-copy alias slices too: callers hold this table for the process
	// lifetime and must not be able to edit registry metadata through it.
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
	}
	return out
}

// findCommand resolves a command token case-insensitively against names and
// aliases. Tokens are matched literally, so a bare "exit" resolves through the
// /exit spec's alias while an unregistered bare word never resolves.
func findCommand(token string) *CommandSpec {
	lower := strings.ToLower(token)
	for i := range commandRegistry {
		spec := &commandRegistry[i]
		if lower == strings.ToLower(spec.Name) {
			return spec
		}
		for _, alias := range spec.Aliases {
			if lower == strings.ToLower(alias) {
				return spec
			}
		}
	}
	return nil
}

// commandHelpText renders /help from the registry: one line per command, the
// usage column sized to the widest entry. No command list is maintained by
// hand anywhere else.
func commandHelpText() string {
	maxUsage := 0
	for _, spec := range commandRegistry {
		if w := len([]rune(spec.Usage)); w > maxUsage {
			maxUsage = w
		}
	}
	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for _, spec := range commandRegistry {
		pad := strings.Repeat(" ", maxUsage-len([]rune(spec.Usage))+1)
		sb.WriteString(fmt.Sprintf("  %s%s— %s\n", spec.Usage, pad, spec.Description))
	}
	sb.WriteString("\nTip: Ctrl+K opens the command palette; Tab completes command names and arguments.")
	return sb.String()
}

// HandleCommand processes slash commands (and exit/quit). Returns Handled=true if
// the input was a command, or Handled=false if it should be sent to the LLM.
func HandleCommand(ctx context.Context, cmdCtx CommandContext, input string) CommandResult {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return CommandResult{Handled: false}
	}

	parts := strings.Fields(trimmed)
	// A bare alias is only a command when it is the whole input: "exit" quits,
	// "exit now" is still a prompt for the model.
	if !strings.HasPrefix(parts[0], "/") && len(parts) > 1 {
		return CommandResult{Handled: false}
	}
	if spec := findCommand(parts[0]); spec != nil {
		arg := strings.TrimSpace(strings.TrimPrefix(trimmed, parts[0]))
		cmdCtx.Ctx = ctx
		return spec.Handle(cmdCtx, arg)
	}

	if !strings.HasPrefix(strings.ToLower(trimmed), "/") {
		return CommandResult{Handled: false}
	}

	return CommandResult{Handled: true, Error: fmt.Errorf("unknown command %q — type /help to see available commands", input)}
}

func handleHelp(CommandContext, string) CommandResult {
	return CommandResult{Handled: true, Output: commandHelpText()}
}

func handleClear(cmdCtx CommandContext, _ string) CommandResult {
	cmdCtx.Session.Clear()
	opts := DefaultPromptOptions(cmdCtx.WorkspaceRoot, cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
	opts.Scratchpad = cmdCtx.Session.Scratchpad()
	cmdCtx.Session.AddMessage(provider.Message{
		Role:    "system",
		Content: BuildSystemPromptWithOptions(opts),
	})
	return CommandResult{Handled: true, Output: "Session history cleared."}
}

func handleCompact(cmdCtx CommandContext, _ string) CommandResult {
	ctx := withCompactTrigger(cmdCtx.Context(), "manual")
	if cmdCtx.ContextManager == nil {
		cmdCtx.ContextManager = NewContextManager(0)
	}
	if cmdCtx.Registry != nil {
		cmdCtx.ContextManager.ResolveBudget(cmdCtx.Registry, cmdCtx.Session.Model())
	}
	// manual /compact fires PreCompact/PostCompact with trigger "manual".
	sessionID := ""
	if cmdCtx.Session != nil {
		sessionID = cmdCtx.Session.ID()
	}
	cmdCtx.ContextManager.PreCompact = preCompactHookFunc(cmdCtx.Hooks, sessionID, cmdCtx.WorkspaceRoot, nil)
	cmdCtx.ContextManager.PostCompact = postCompactHookFunc(cmdCtx.Hooks, sessionID, cmdCtx.WorkspaceRoot, nil)
	// restate the live plan/todo list in the manual summary prompt too.
	cmdCtx.ContextManager.PlanSnapshot = func() string {
		if cmdCtx.Plan != nil {
			return cmdCtx.Plan.Render()
		}
		return ""
	}

	history := cmdCtx.Session.History()
	var compacted []provider.Message
	notice := ""
	usedSummary := false

	var active provider.Provider
	if cmdCtx.Registry != nil {
		if p, err := cmdCtx.Registry.Active(); err == nil {
			active = p
		}
	}
	if active != nil {
		summarized, err := cmdCtx.ContextManager.CompactWithSummary(ctx, active, cmdCtx.Session.Model(), history, 2)
		switch {
		case err == nil:
			compacted = summarized
			usedSummary = true
		case errors.Is(err, ErrCompactBlockedByHook):
			// A PreCompact deny blocks compaction entirely: history stays
			// exactly as it was, with no placeholder fallback.
			reason := strings.TrimPrefix(err.Error(), ErrCompactBlockedByHook.Error()+": ")
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("Compaction blocked by hook: %s", reason),
			}
		default:
			notice = fmt.Sprintf("\nProvider summary unavailable (%v); used placeholder compaction.", err)
		}
	}
	if !usedSummary {
		compacted = cmdCtx.ContextManager.Compact(history, 2)
	}

	cmdCtx.Session.LoadMessages(compacted)
	// anchor replay at the manual summary so a resumed session matches.
	if usedSummary {
		cmdCtx.Session.RecordSummaryAnchor(compacted)
	}
	if usedSummary && cmdCtx.ContextManager.PostCompact != nil {
		cmdCtx.ContextManager.PostCompact(ctx, "manual")
	}
	return CommandResult{
		Handled: true,
		Output: fmt.Sprintf("Compacted history: %d messages -> %d messages.\n%s%s",
			len(history), len(compacted), cmdCtx.ContextManager.FormatContextStats(compacted), notice),
	}
}

func handleStats(cmdCtx CommandContext, _ string) CommandResult {
	if cmdCtx.ContextManager == nil {
		cmdCtx.ContextManager = NewContextManager(0)
	}
	if cmdCtx.Registry != nil {
		cmdCtx.ContextManager.ResolveBudget(cmdCtx.Registry, cmdCtx.Session.Model())
	}
	return CommandResult{
		Handled: true,
		Output:  cmdCtx.ContextManager.FormatContextStats(cmdCtx.Session.History()),
	}
}

func handleNew(cmdCtx CommandContext, _ string) CommandResult {
	st := cmdCtx.Session.Store()
	var newID string
	if st != nil {
		newID = session.GenerateID()
		rec, err := st.CreateSessionWithWorkspace(newID, "", cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model(), cmdCtx.WorkspaceRoot)
		if err == nil {
			cmdCtx.Session.SetID(rec.ID)
		}
	}
	cmdCtx.Session.Clear()
	cmdCtx.Session.SetScratchpad("")
	opts := DefaultPromptOptions(cmdCtx.WorkspaceRoot, cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
	cmdCtx.Session.AddMessage(provider.Message{
		Role:    "system",
		Content: BuildSystemPromptWithOptions(opts),
	})
	if newID != "" {
		return CommandResult{Handled: true, Output: fmt.Sprintf("Started new session: %s", newID)}
	}
	return CommandResult{Handled: true, Output: "Started new session."}
}

func handleSessions(cmdCtx CommandContext, arg string) CommandResult {
	if arg != "" {
		return resumeSession(cmdCtx, arg)
	}
	st := cmdCtx.Session.Store()
	if st == nil {
		return CommandResult{Handled: true, Output: "Session store is not enabled."}
	}
	summaries, err := st.ListSessions(20)
	if err != nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("error listing sessions: %w", err)}
	}
	if len(summaries) == 0 {
		return CommandResult{Handled: true, Output: "No saved sessions found."}
	}

	var sb strings.Builder
	sb.WriteString("Saved Sessions:\n")
	sb.WriteString(strings.Repeat("─", 65))
	sb.WriteString("\n")
	for _, sum := range summaries {
		marker := "  "
		if sum.ID == cmdCtx.Session.ID() {
			marker = "* "
		}
		sb.WriteString(fmt.Sprintf("%s%-24s | %-10s | %-12s | %2d msgs | %s\n",
			marker, sum.ID, sum.Provider, sum.Model, sum.MessageCount, sum.UpdatedAt.Local().Format("Jan 02 15:04")))
	}
	sb.WriteString(strings.Repeat("─", 65))
	sb.WriteString("\nResume a session with: /sessions <id> or /resume <id>")
	return CommandResult{Handled: true, Output: sb.String()}
}

func handleResume(cmdCtx CommandContext, arg string) CommandResult {
	if arg == "" {
		return CommandResult{Handled: true, Output: "Usage: /resume <session-id>"}
	}
	return resumeSession(cmdCtx, arg)
}

func handleCommit(cmdCtx CommandContext, arg string) CommandResult {
	return executeCommit(cmdCtx, arg)
}

func handleProviders(cmdCtx CommandContext, _ string) CommandResult {
	ctx := cmdCtx.Context()
	active := cmdCtx.Registry.ActiveName()
	available := cmdCtx.Registry.List()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Active Provider: %s\nAvailable Providers: %s\n", active, strings.Join(available, ", ")))
	if activeProv, err := cmdCtx.Registry.Active(); err == nil && activeProv != nil {
		if models, err := activeProv.Models(ctx); err == nil && len(models) > 0 {
			sb.WriteString(fmt.Sprintf("Available Models for %s: %s", active, strings.Join(models, ", ")))
		}
	}
	return CommandResult{Handled: true, Output: sb.String()}
}

func handleProvider(cmdCtx CommandContext, arg string) CommandResult {
	ctx := cmdCtx.Context()
	if arg == "" {
		return CommandResult{Handled: true, Output: fmt.Sprintf("Active Provider: %s (use '/provider <name>' to switch)", cmdCtx.Registry.ActiveName())}
	}
	if err := cmdCtx.Registry.Switch(arg); err != nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("error switching provider: %w", err)}
	}
	cmdCtx.Session.SetProvider(arg)
	out := fmt.Sprintf("Switched provider to %s.", arg)
	if activeProv, err := cmdCtx.Registry.Active(); err == nil && activeProv != nil {
		if models, err := activeProv.Models(ctx); err == nil && len(models) > 0 {
			out += fmt.Sprintf("\nAvailable models: %s", strings.Join(models, ", "))
		}
	}
	return CommandResult{Handled: true, Output: out}
}

func handleModel(cmdCtx CommandContext, arg string) CommandResult {
	ctx := cmdCtx.Context()
	if arg == "" {
		out := fmt.Sprintf("Active Model: %s", cmdCtx.Session.Model())
		if activeProv, err := cmdCtx.Registry.Active(); err == nil && activeProv != nil {
			if models, err := activeProv.Models(ctx); err == nil && len(models) > 0 {
				out += fmt.Sprintf("\nAvailable models (%s): %s", cmdCtx.Registry.ActiveName(), strings.Join(models, ", "))
			}
		}
		return CommandResult{Handled: true, Output: out}
	}
	if cmdCtx.Registry == nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("no provider registry configured")}
	}
	activeProv, err := cmdCtx.Registry.Active()
	if err != nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("cannot validate model: active provider unavailable: %w", err)}
	}
	if activeProv == nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("cannot validate model: no active provider")}
	}
	models, err := activeProv.Models(ctx)
	if err != nil {
		return CommandResult{Handled: true, Error: fmt.Errorf("cannot validate model %q: model list unavailable: %w", arg, err)}
	}
	if len(models) == 0 {
		return CommandResult{Handled: true, Error: fmt.Errorf("cannot validate model %q: provider returned an empty model list", arg)}
	}
	found := false
	for _, m := range models {
		if m == arg {
			found = true
			break
		}
	}
	if !found {
		return CommandResult{
			Handled: true,
			Error:   fmt.Errorf("model %q is not available on %s — available: %s", arg, cmdCtx.Registry.ActiveName(), strings.Join(models, ", ")),
		}
	}
	cmdCtx.Session.SetModel(arg)
	return CommandResult{Handled: true, Output: fmt.Sprintf("Model set to %s.", arg)}
}

func handleExit(CommandContext, string) CommandResult {
	return CommandResult{Handled: true, Exit: true, Output: "Goodbye!"}
}

func resumeSession(cmdCtx CommandContext, sessionID string) CommandResult {
	st := cmdCtx.Session.Store()
	if st == nil {
		return CommandResult{Handled: true, Output: "Session store is not enabled."}
	}
	rec, err := st.GetSession(sessionID)
	if err != nil || rec == nil {
		return CommandResult{Handled: true, Output: fmt.Sprintf("Session %q not found.", sessionID)}
	}
	cmdCtx.Session.SetID(rec.ID)
	cmdCtx.Session.SetProvider(rec.Provider)
	cmdCtx.Session.SetModel(rec.Model)
	cmdCtx.Session.SetScratchpad(rec.Scratchpad)
	cmdCtx.Session.LoadMessages(rec.Messages)
	if rec.Provider != "" {
		_ = cmdCtx.Registry.Switch(rec.Provider)
	}
	// The stored transcript carries the prompt of the workspace it was recorded
	// in; this process runs in a possibly different one, so rebuild it.
	opts := DefaultPromptOptions(cmdCtx.WorkspaceRoot, cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
	opts.Scratchpad = cmdCtx.Session.Scratchpad()
	cmdCtx.Session.SetSystemMessage(BuildSystemPromptWithOptions(opts))
	return CommandResult{
		Handled: true,
		Output:  fmt.Sprintf("Resumed session %s (%d messages, model: %s/%s)", rec.ID, len(rec.Messages), rec.Provider, rec.Model),
	}
}

// gitAddArgs builds the argument vector for staging a single path safely:
// the "--" separator stops a path like "-n" being read as a flag.
func gitAddArgs(relPath string) []string {
	return []string{"add", "--", relPath}
}

// gitCommitArgs limits the commit to exactly the session's files. Without a
// pathspec, `git commit` would sweep in unrelated staged changes.
func gitCommitArgs(msg string, relPaths []string) []string {
	args := []string{"commit", "-m", msg, "--"}
	return append(args, relPaths...)
}

func executeCommit(cmdCtx CommandContext, messageArg string) CommandResult {
	files := cmdCtx.Session.ModifiedFiles()
	if len(files) == 0 {
		return CommandResult{Handled: true, Output: "No modified files tracked in this session."}
	}

	workDir := cmdCtx.WorkspaceRoot
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	msg := messageArg
	if msg == "" {
		msg = "Changes assisted by GoCode"
	}
	var relPaths []string
	trailer := fmt.Sprintf("Assisted-by: GoCode:%s", cmdCtx.Session.Model())
	fullCommitMsg := fmt.Sprintf("%s\n\n%s", msg, trailer)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Preparing to commit %d file(s):\n", len(files)))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("  • %s\n", f))
	}
	sb.WriteString(fmt.Sprintf("\nCommit message:\n%s\n", fullCommitMsg))

	if cmdCtx.AskApproval != nil {
		approved := cmdCtx.AskApproval(sb.String() + "\nApprove this commit? [y/N]")
		if !approved {
			return CommandResult{Handled: true, Output: "Commit cancelled by user."}
		}
	}

	for _, file := range files {
		relPath := file
		if filepath.IsAbs(file) {
			if rel, err := filepath.Rel(workDir, file); err == nil {
				relPath = rel
			}
		}
		relPaths = append(relPaths, relPath)
		addCmd := exec.Command("git", gitAddArgs(relPath)...)
		addCmd.Dir = workDir
		if out, err := addCmd.CombinedOutput(); err != nil {
			return CommandResult{
				Handled: true,
				Error:   fmt.Errorf("git add %s failed: %v\n%s", relPath, err, string(out)),
			}
		}
	}

	commitCmd := exec.Command("git", gitCommitArgs(fullCommitMsg, relPaths)...)
	commitCmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	commitCmd.Stdout = &stdout
	commitCmd.Stderr = &stderr
	if err := commitCmd.Run(); err != nil {
		return CommandResult{
			Handled: true,
			Error:   fmt.Errorf("git commit failed: %v\n%s\n%s", err, stdout.String(), stderr.String()),
		}
	}

	return CommandResult{
		Handled: true,
		Output:  fmt.Sprintf("Commit successful!\n%s", stdout.String()),
	}
}
