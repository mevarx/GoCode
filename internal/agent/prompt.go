package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mevarx/GoCode/internal/config"
)

const baseSystemPrompt = `You are GoCode, an expert terminal AI coding assistant. You help users inspect, write, test, and debug code.
You have access to tools for executing shell commands, searching code, and reading/writing/patching files.
Be concise, direct, and pragmatic.

Guidelines:
- Workspace boundary: Work strictly within the designated workspace directory. Never access files outside it.
- Sensitive files: Never attempt to read, write, or leak secrets, environment variables (.env*), credentials, or private keys.
- Code search: Use code_search to find symbols, definitions, and patterns across the codebase before making changes.
- Reading files: Use file_read with offset and limit parameters when examining large files.
- Editing files: Prefer file_patch for targeted edits over rewriting entire files with file_write.
- Verification: Always run tests or build checks using shell_exec to verify changes before completing tasks.
- Problem solving: If a command or tool fails, explain why and diagnose before retrying. Avoid infinite loops.`

// PromptOptions provides runtime context and custom instructions for prompt construction.
type PromptOptions struct {
	ProjectContext string
	GlobalContext  string
	WorkspaceRoot  string
	OS             string
	Provider       string
	Model          string
}

// FindProjectContext scans startDir and parent directories for AGENTS.md, .gocode/AGENTS.md, or docs/AGENTS.md.
func FindProjectContext(startDir string) (string, string, error) {
	if startDir == "" {
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("failed to get current working directory: %w", err)
		}
	}

	curr, err := filepath.Abs(startDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to get absolute path of %s: %w", startDir, err)
	}

	candidates := []string{
		"AGENTS.md",
		filepath.Join(".gocode", "AGENTS.md"),
		filepath.Join("docs", "AGENTS.md"),
	}

	for {
		for _, candidate := range candidates {
			candidatePath := filepath.Join(curr, candidate)
			if data, err := os.ReadFile(candidatePath); err == nil {
				return strings.TrimSpace(string(data)), candidatePath, nil
			}
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	return "", "", nil
}

// LoadGlobalContext loads ~/.config/gocode/CONTEXT.md or %APPDATA%\gocode\CONTEXT.md.
func LoadGlobalContext() (string, string, error) {
	configDir := config.ConfigDir()
	globalPath := filepath.Join(configDir, "CONTEXT.md")

	data, err := os.ReadFile(globalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", globalPath, nil
		}
		return "", globalPath, fmt.Errorf("failed to read global context file %s: %w", globalPath, err)
	}

	return strings.TrimSpace(string(data)), globalPath, nil
}

// BuildSystemPrompt constructs the complete system prompt with project and global context.
func BuildSystemPrompt(projectContext, globalContext string) string {
	return BuildSystemPromptWithOptions(PromptOptions{
		ProjectContext: projectContext,
		GlobalContext:  globalContext,
	})
}

// BuildSystemPromptWithOptions constructs the system prompt with rich runtime context.
func BuildSystemPromptWithOptions(opts PromptOptions) string {
	var sb strings.Builder

	if strings.TrimSpace(opts.ProjectContext) != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(strings.TrimSpace(opts.ProjectContext))
		sb.WriteString("\n\n")
	}

	sb.WriteString(baseSystemPrompt)

	// Runtime Environment context
	var runtimeItems []string
	if opts.OS != "" {
		runtimeItems = append(runtimeItems, fmt.Sprintf("OS: %s", opts.OS))
	}
	if opts.WorkspaceRoot != "" {
		runtimeItems = append(runtimeItems, fmt.Sprintf("Workspace: %s", opts.WorkspaceRoot))
		if branch := getGitBranch(opts.WorkspaceRoot); branch != "" {
			runtimeItems = append(runtimeItems, fmt.Sprintf("Git Branch: %s", branch))
		}
	}
	if opts.Provider != "" {
		runtimeItems = append(runtimeItems, fmt.Sprintf("Provider: %s", opts.Provider))
	}
	if opts.Model != "" {
		runtimeItems = append(runtimeItems, fmt.Sprintf("Model: %s", opts.Model))
	}

	if len(runtimeItems) > 0 {
		sb.WriteString("\n\n## Environment\n")
		for _, item := range runtimeItems {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		// Trim trailing newline for clean spacing
		content := strings.TrimRight(sb.String(), "\n")
		sb.Reset()
		sb.WriteString(content)
	}

	if strings.TrimSpace(opts.GlobalContext) != "" {
		sb.WriteString("\n\n## Global Context\n")
		sb.WriteString(strings.TrimSpace(opts.GlobalContext))
	}

	return sb.String()
}

// getGitBranch attempts to inspect .git/HEAD to find the current branch without invoking git.
func getGitBranch(workspaceRoot string) string {
	headPath := filepath.Join(workspaceRoot, ".git", "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if strings.HasPrefix(content, "ref: refs/heads/") {
		return strings.TrimPrefix(content, "ref: refs/heads/")
	}
	if len(content) >= 7 {
		return content[:7] // detached HEAD commit hash
	}
	return ""
}

// DefaultPromptOptions creates a PromptOptions prefilled with host OS and discovered context.
func DefaultPromptOptions(workspaceRoot, provider, model string) PromptOptions {
	projCtx, _, _ := FindProjectContext(workspaceRoot)
	globCtx, _, _ := LoadGlobalContext()
	return PromptOptions{
		ProjectContext: projCtx,
		GlobalContext:  globCtx,
		WorkspaceRoot:  workspaceRoot,
		OS:             fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Provider:       provider,
		Model:          model,
	}
}
