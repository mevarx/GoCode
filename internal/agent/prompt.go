package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/config"
)

const baseSystemPrompt = `You are GoCode, a helpful coding assistant running in the terminal. You help users with programming tasks.
You have access to tools for executing shell commands and reading/writing files.
Be concise and direct. When you need to perform actions, use the available tools.`

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
	var sb strings.Builder

	if strings.TrimSpace(projectContext) != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(strings.TrimSpace(projectContext))
		sb.WriteString("\n\n")
	}

	sb.WriteString(baseSystemPrompt)

	if strings.TrimSpace(globalContext) != "" {
		sb.WriteString("\n\n## Global Context\n")
		sb.WriteString(strings.TrimSpace(globalContext))
	}

	return sb.String()
}
