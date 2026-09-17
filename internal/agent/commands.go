package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

// CommandContext holds the environment and services needed to execute slash commands.
type CommandContext struct {
	Session        *Session
	Registry       *provider.Registry
	ContextManager *ContextManager
	WorkspaceRoot  string
	AskApproval    func(prompt string) bool
}

// CommandResult represents the outcome of executing a command.
type CommandResult struct {
	Handled bool
	Output  string
	Error   error
	Exit    bool
}

// HandleCommand processes slash commands (and exit/quit). Returns Handled=true if
// the input was a command, or Handled=false if it should be sent to the LLM.
func HandleCommand(ctx context.Context, cmdCtx CommandContext, input string) CommandResult {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return CommandResult{Handled: false}
	}

	lower := strings.ToLower(trimmed)

	// Check exit / quit
	if lower == "exit" || lower == "quit" {
		return CommandResult{Handled: true, Exit: true, Output: "Goodbye!"}
	}

	if !strings.HasPrefix(lower, "/") {
		return CommandResult{Handled: false}
	}

	parts := strings.Fields(trimmed)
	cmd := strings.ToLower(parts[0])
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, parts[0]))

	switch cmd {
	case "/help":
		return CommandResult{
			Handled: true,
			Output: `Available commands:
  /help            — Show this help message
  /clear           — Clear conversation history and reset context
  /compact         — Compact earlier turns in conversation history
  /stats           — Show current token usage and message count
  /new             — Start a new session
  /sessions        — List previous saved sessions
  /sessions <id>   — Resume session by ID
  /resume <id>     — Resume session by ID
  /commit [msg]    — Commit modified files with attribution trailer
  /providers       — List all available providers and their models
  /provider <name> — Switch active provider
  /model           — Show current model
  /model <name>    — Switch model`,
		}

	case "/clear":
		cmdCtx.Session.Clear()
		opts := DefaultPromptOptions(cmdCtx.WorkspaceRoot, cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
		cmdCtx.Session.AddMessage(provider.Message{
			Role:    "system",
			Content: BuildSystemPromptWithOptions(opts),
		})
		return CommandResult{Handled: true, Output: "Session history cleared."}

	case "/compact":
		if cmdCtx.ContextManager == nil {
			cmdCtx.ContextManager = NewContextManager(0)
		}
		history := cmdCtx.Session.History()
		compacted := cmdCtx.ContextManager.Compact(history, 2)
		cmdCtx.Session.LoadMessages(compacted)
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Compacted history: %d messages -> %d messages.\n%s", len(history), len(compacted), cmdCtx.ContextManager.FormatContextStats(compacted)),
		}

	case "/stats":
		if cmdCtx.ContextManager == nil {
			cmdCtx.ContextManager = NewContextManager(0)
		}
		return CommandResult{
			Handled: true,
			Output:  cmdCtx.ContextManager.FormatContextStats(cmdCtx.Session.History()),
		}

	case "/new":
		st := cmdCtx.Session.Store()
		var newID string
		if st != nil {
			newID = session.GenerateID()
			rec, err := st.CreateSession(newID, "", cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
			if err == nil {
				cmdCtx.Session.SetID(rec.ID)
			}
		}
		cmdCtx.Session.Clear()
		opts := DefaultPromptOptions(cmdCtx.WorkspaceRoot, cmdCtx.Registry.ActiveName(), cmdCtx.Session.Model())
		cmdCtx.Session.AddMessage(provider.Message{
			Role:    "system",
			Content: BuildSystemPromptWithOptions(opts),
		})
		if newID != "" {
			return CommandResult{Handled: true, Output: fmt.Sprintf("Started new session: %s", newID)}
		}
		return CommandResult{Handled: true, Output: "Started new session."}

	case "/sessions":
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

	case "/resume":
		if arg == "" {
			return CommandResult{Handled: true, Output: "Usage: /resume <session-id>"}
		}
		return resumeSession(cmdCtx, arg)

	case "/commit":
		return executeCommit(cmdCtx, arg)

	case "/providers":
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

	case "/provider":
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

	case "/model":
		if arg == "" {
			out := fmt.Sprintf("Active Model: %s", cmdCtx.Session.Model())
			if activeProv, err := cmdCtx.Registry.Active(); err == nil && activeProv != nil {
				if models, err := activeProv.Models(ctx); err == nil && len(models) > 0 {
					out += fmt.Sprintf("\nAvailable models (%s): %s", cmdCtx.Registry.ActiveName(), strings.Join(models, ", "))
				}
			}
			return CommandResult{Handled: true, Output: out}
		}
		if cmdCtx.Registry != nil {
			if activeProv, err := cmdCtx.Registry.Active(); err == nil && activeProv != nil {
				if models, err := activeProv.Models(ctx); err == nil && len(models) > 0 {
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
				}
			}
		}
		cmdCtx.Session.SetModel(arg)
		return CommandResult{Handled: true, Output: fmt.Sprintf("Model set to %s.", arg)}

	default:
		return CommandResult{Handled: true, Error: fmt.Errorf("unknown command %q — type /help to see available commands", input)}
	}
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
	cmdCtx.Session.LoadMessages(rec.Messages)
	if rec.Provider != "" {
		_ = cmdCtx.Registry.Switch(rec.Provider)
	}
	return CommandResult{
		Handled: true,
		Output:  fmt.Sprintf("Resumed session %s (%d messages, model: %s/%s)", rec.ID, len(rec.Messages), rec.Provider, rec.Model),
	}
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
	trailer := fmt.Sprintf("Assisted-by: GoCode:%s", cmdCtx.Session.Model())
	fullCommitMsg := fmt.Sprintf("%s\n\n%s", msg, trailer)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Preparing to commit %d file(s):\n", len(files)))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("  • %s\n", f))
	}
	sb.WriteString(fmt.Sprintf("\nCommit message:\n%s\n", fullCommitMsg))

	if cmdCtx.AskApproval != nil {
		approved := cmdCtx.AskApproval(fmt.Sprintf("Commit %d modified file(s)?", len(files)))
		if !approved {
			return CommandResult{Handled: true, Output: "Commit cancelled by user."}
		}
	}

	// Run git add for each modified file
	for _, file := range files {
		relPath := file
		if filepath.IsAbs(file) {
			if rel, err := filepath.Rel(workDir, file); err == nil {
				relPath = rel
			}
		}
		addCmd := exec.Command("git", "add", relPath)
		addCmd.Dir = workDir
		if out, err := addCmd.CombinedOutput(); err != nil {
			return CommandResult{
				Handled: true,
				Error:   fmt.Errorf("git add %s failed: %v\n%s", relPath, err, string(out)),
			}
		}
	}

	// Run git commit
	commitCmd := exec.Command("git", "commit", "-m", fullCommitMsg)
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
