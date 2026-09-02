package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

func Run(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
) error {
	bridge := NewApprovalBridge()
	approval.OnPresent = bridge.RequestApproval

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 256)
	cancelCh := make(chan struct{}, 1)

	tuiCtx, tuiCancel := context.WithCancel(ctx)
	defer tuiCancel()

	var pickerItems []list.Item
	for _, pName := range registry.List() {
		p := registry.Get(pName)
		if p != nil {
			if models, err := p.Models(ctx); err == nil && len(models) > 0 {
				for _, m := range models {
					pickerItems = append(pickerItems, ModelItem{
						Provider: pName,
						Model:    m,
					})
				}
			}
		}
	}
	if len(pickerItems) == 0 {
		pickerItems = append(pickerItems, ModelItem{
			Provider: registry.ActiveName(),
			Model:    session.Model(),
		})
	}

	m := NewModel(registry.ActiveName(), session.Model(), "0.3.0", bridge, inputCh, outputCh, cancelCh, pickerItems)

	go runAgentGoroutine(tuiCtx, registry, session, toolRegistry, approval, inputCh, outputCh, cancelCh)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	tuiCancel()
	return err
}

func sendMsg(ctx context.Context, outputCh chan<- tea.Msg, msg tea.Msg) {
	select {
	case outputCh <- msg:
	case <-ctx.Done():
	}
}

func runAgentGoroutine(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	inputCh <-chan string,
	outputCh chan<- tea.Msg,
	cancelCh <-chan struct{},
) {
	toolSpecs := toolSpecsAsProvider(toolRegistry)

	for {
		select {
		case <-ctx.Done():
			return
		case input := <-inputCh:
			turnCtx, turnCancel := context.WithCancel(ctx)
			cancelWatcherDone := make(chan struct{})
			go watchCancelSignal(cancelCh, turnCtx, turnCancel, cancelWatcherDone)

			if handled := handleSlashCommand(turnCtx, input, registry, session, outputCh); handled {
				sendMsg(ctx, outputCh, agentDoneMsg{})
				turnCancel()
				<-cancelWatcherDone
				continue
			}

			session.AddMessage(provider.Message{Role: "user", Content: input})

			err := runTurn(turnCtx, registry, session, toolRegistry, approval, toolSpecs, outputCh)
			sendMsg(ctx, outputCh, agentDoneMsg{err: err})
			turnCancel()
			<-cancelWatcherDone
		}
	}
}

// watchCancelSignal cancels the active turn when the user interrupts, until
// the turn itself ends. It must exit before the next turn consumes the next
// interrupt signal, hence the done channel join.
func watchCancelSignal(cancelCh <-chan struct{}, turnCtx context.Context, turnCancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	select {
	case <-cancelCh:
		turnCancel()
	case <-turnCtx.Done():
	}
}

func runTurn(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	toolSpecs []provider.ToolSpec,
	outputCh chan<- tea.Msg,
) error {
	for {
		ch, err := registry.Active().Stream(ctx, session.Model(), session.History(), toolSpecs)
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		var fullResponse strings.Builder
		var toolCalls []provider.ToolCall

		for chunk := range ch {
			if chunk.Err != nil {
				return fmt.Errorf("stream chunk error: %w", chunk.Err)
			}
			if chunk.Delta != "" {
				fullResponse.WriteString(chunk.Delta)
				sendMsg(ctx, outputCh, agentChunkMsg{delta: chunk.Delta})
			}
			if len(chunk.ToolCalls) > 0 {
				toolCalls = append(toolCalls, chunk.ToolCalls...)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		session.AddMessage(provider.Message{
			Role:      "assistant",
			Content:   fullResponse.String(),
			ToolCalls: toolCalls,
		})

		if len(toolCalls) == 0 {
			return nil
		}

		if err := handleToolCalls(ctx, toolCalls, toolRegistry, approval, session, outputCh); err != nil {
			return err
		}
	}
}

func handleToolCalls(
	ctx context.Context,
	toolCalls []provider.ToolCall,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	session *agent.Session,
	outputCh chan<- tea.Msg,
) error {
	for _, tc := range toolCalls {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		tool := toolRegistry.Get(tc.Name)
		if tool == nil {
			errMsg := fmt.Sprintf("unknown tool %q", tc.Name)
			session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    "Error: " + errMsg,
				ToolCallID: tc.ID,
			})
			sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: errMsg, isError: true})
			continue
		}

		result, err := approval.WrapExecution(ctx, tool, json.RawMessage(tc.Args))
		if err != nil {
			errContent := fmt.Sprintf("execution error: %v", err)
			session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    errContent,
				ToolCallID: tc.ID,
			})
			sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: errContent, isError: true})
			continue
		}

		content := result.String()
		if result.Diff != "" {
			content = content + "\n\nDiff:\n" + result.Diff
		}
		session.AddMessage(provider.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: tc.ID,
		})

		display := result.Output
		if result.Error != "" {
			display = result.Error
		}
		if len(display) > 2000 {
			display = display[:2000] + "\n… (truncated)"
		}
		sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: display, isError: result.Error != ""})
	}

	return nil
}

func handleSlashCommand(
	ctx context.Context,
	input string,
	registry *provider.Registry,
	session *agent.Session,
	outputCh chan<- tea.Msg,
) bool {
	lower := strings.ToLower(strings.TrimSpace(input))

	switch {
	case lower == "/clear":
		session.Clear()
		projCtx, _, _ := agent.FindProjectContext("")
		globCtx, _, _ := agent.LoadGlobalContext()
		session.AddMessage(provider.Message{
			Role:    "system",
			Content: agent.BuildSystemPrompt(projCtx, globCtx),
		})
		sendMsg(ctx, outputCh, agentToolMsg{name: "Session", result: "cleared"})
		return true

	case lower == "/new":
		st := session.Store()
		if st != nil {
			rec, err := st.CreateSession("", "", registry.ActiveName(), session.Model())
			if err == nil {
				session.SetID(rec.ID)
				session.Clear()
				projCtx, _, _ := agent.FindProjectContext("")
				globCtx, _, _ := agent.LoadGlobalContext()
				session.AddMessage(provider.Message{
					Role:    "system",
					Content: agent.BuildSystemPrompt(projCtx, globCtx),
				})
				sendMsg(ctx, outputCh, agentToolMsg{name: "Session", result: fmt.Sprintf("Started new session: %s", rec.ID)})
				return true
			}
		}
		session.Clear()
		sendMsg(ctx, outputCh, agentToolMsg{name: "Session", result: "Started new session"})
		return true

	case lower == "/sessions":
		st := session.Store()
		if st == nil {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: "Session store is not enabled.", isError: true})
			return true
		}
		summaries, err := st.ListSessions(20)
		if err != nil {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: fmt.Sprintf("Error: %v", err), isError: true})
			return true
		}
		if len(summaries) == 0 {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: "No saved sessions found."})
			return true
		}
		var sb strings.Builder
		sb.WriteString("Saved Sessions:\n")
		for _, sum := range summaries {
			activeMarker := "  "
			if sum.ID == session.ID() {
				activeMarker = "* "
			}
			sb.WriteString(fmt.Sprintf("%s%-22s | %-9s | %-12s | %2d msgs | %s\n",
				activeMarker, sum.ID, sum.Provider, sum.Model, sum.MessageCount, sum.UpdatedAt.Local().Format("Jan 02 15:04")))
		}
		sb.WriteString("\nResume with: /sessions <id> or /resume <id>")
		sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: sb.String()})
		return true

	case strings.HasPrefix(lower, "/sessions ") || strings.HasPrefix(lower, "/resume "):
		targetID := strings.TrimSpace(input[strings.Index(input, " "):])
		st := session.Store()
		if st == nil {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: "Session store is not enabled.", isError: true})
			return true
		}
		rec, err := st.GetSession(targetID)
		if err != nil || rec == nil {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Sessions", result: fmt.Sprintf("Session %q not found.", targetID), isError: true})
			return true
		}
		session.SetID(rec.ID)
		session.SetProvider(rec.Provider)
		session.SetModel(rec.Model)
		session.LoadMessages(rec.Messages)
		if rec.Provider != "" {
			_ = registry.Switch(rec.Provider)
		}
		sendMsg(ctx, outputCh, agentToolMsg{name: "Session", result: fmt.Sprintf("Resumed session %s (%d messages, model: %s/%s)", rec.ID, len(rec.Messages), rec.Provider, rec.Model)})
		return true

	case strings.HasPrefix(lower, "/commit"):
		msg := strings.TrimSpace(strings.TrimPrefix(input, "/commit"))
		if msg == "" {
			msg = "Changes assisted by GoCode"
		}
		files := session.ModifiedFiles()
		if len(files) == 0 {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Git Attribution", result: "No modified files tracked in this session."})
			return true
		}
		trailer := fmt.Sprintf("Assisted-by: GoCode:%s", session.Model())
		fullCommitMsg := fmt.Sprintf("%s\n\n%s", msg, trailer)
		info := fmt.Sprintf("Modified files (%d):\n%s\n\nCommit trailer:\n%s", len(files), strings.Join(files, "\n"), fullCommitMsg)
		sendMsg(ctx, outputCh, agentToolMsg{name: "Git Attribution", result: info})
		return true

	case lower == "/providers" || lower == "/provider":
		info := fmt.Sprintf("Active: %s | Available: %s", registry.ActiveName(), strings.Join(registry.List(), ", "))
		if models, err := registry.Active().Models(ctx); err == nil && len(models) > 0 {
			info += "\nModels: " + strings.Join(models, ", ")
		}
		sendMsg(ctx, outputCh, agentToolMsg{name: "Providers", result: info})
		return true

	case strings.HasPrefix(lower, "/provider "):
		target := strings.TrimSpace(input[10:])
		if err := registry.Switch(target); err != nil {
			sendMsg(ctx, outputCh, agentToolMsg{name: "Provider", result: fmt.Sprintf("error: %v", err), isError: true})
		} else {
			session.SetProvider(target)
			sendMsg(ctx, outputCh, agentToolMsg{name: "Provider", result: fmt.Sprintf("switched to %s", target)})
		}
		return true

	case lower == "/model" || strings.HasPrefix(lower, "/model "):
		target := strings.TrimSpace(strings.TrimPrefix(lower, "/model"))
		if target == "" {
			info := "Active model: " + session.Model()
			if models, err := registry.Active().Models(ctx); err == nil && len(models) > 0 {
				info += "\nAvailable: " + strings.Join(models, ", ")
			}
			sendMsg(ctx, outputCh, agentToolMsg{name: "Model", result: info})
			return true
		}
		if models, err := registry.Active().Models(ctx); err == nil && len(models) > 0 {
			found := false
			for _, m := range models {
				if m == target {
					found = true
					break
				}
			}
			if !found {
				sendMsg(ctx, outputCh, agentToolMsg{name: "Model", result: fmt.Sprintf("model %q is not available on %s — available: %s", target, registry.ActiveName(), strings.Join(models, ", ")), isError: true})
				return true
			}
		}
		session.SetModel(target)
		sendMsg(ctx, outputCh, agentToolMsg{name: "Model", result: fmt.Sprintf("set to %s", target)})
		return true

	case lower == "/help":
		help := "Available commands:\n" +
			"  /sessions        — list saved sessions\n" +
			"  /sessions <id>   — resume session by ID\n" +
			"  /new             — start a new session\n" +
			"  /commit [msg]    — commit modified files with attribution trailer\n" +
			"  /providers       — list all providers and models\n" +
			"  /provider <name> — switch provider\n" +
			"  /model           — show current model\n" +
			"  /model <name>    — switch model\n" +
			"  /clear           — clear conversation history\n" +
			"  /help            — show this help\n" +
			"  /exit            — quit GoCode\n" +
			"  Ctrl+L           — open interactive model picker\n" +
			"  Ctrl+C           — stop a running turn, or quit when idle\n" +
			"  Esc              — stop a running turn, or clear current input\n" +
			"  exit             — quit GoCode"
		sendMsg(ctx, outputCh, agentToolMsg{name: "Help", result: help})
		return true
	}

	if strings.HasPrefix(lower, "/") {
		sendMsg(ctx, outputCh, agentToolMsg{name: "Command", result: fmt.Sprintf("Unknown command %q — type /help to see available commands", input), isError: true})
		return true
	}

	return false
}

func toolSpecsAsProvider(toolRegistry *tools.Registry) []provider.ToolSpec {
	toolSpecs := toolRegistry.Specs()
	providerSpecs := make([]provider.ToolSpec, len(toolSpecs))
	for i, ts := range toolSpecs {
		providerSpecs[i] = provider.ToolSpec{
			Name:        ts.Name,
			Description: ts.Description,
			Parameters:  ts.Parameters,
		}
	}
	return providerSpecs
}
