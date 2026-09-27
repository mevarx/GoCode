package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

// ShellGuard applies defense-in-depth filtering around shell_exec.
//
// An arbitrary `sh -c` string cannot be reliably confined: command
// substitution, pipes, variables and interpreters can all reach outside the
// workspace no matter how the command line is inspected. GoCode therefore does
// not claim to sandbox the shell. What it can enforce is that secrets which
// come back on stdout/stderr are not handed to the model or written into a
// session transcript, and that commands referencing known-sensitive paths are
// surfaced in the approval preview so the user can decline them.
//
// The file tools remain the hard boundary: those are genuinely confined to the
// workspace by ValidatePath and the sensitive-file matcher.
type ShellGuard struct {
	// SensitiveMatcher identifies paths that should never be referenced by a
	// shell command. May be nil, in which case only secret redaction applies.
	SensitiveMatcher *ignore.SensitiveMatcher
	// RedactSecretOutput disables redaction of secret-shaped values in output.
	RedactSecretOutput bool
}

const redactedMarker = "***REDACTED***"

// secretPatterns match values that look like credentials regardless of the
// file they came from. Order matters only for reporting; each is independent.
var secretPatterns = []struct {
	name  string
	re    *regexp.Regexp
	group int
}{
	{"anthropic-api-key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`), 0},
	{"openai-api-key", regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`), 0},
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`), 0},
	{"google-api-key", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`), 0},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`), 0},
	{"aws-access-key-id", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"private-key-block", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), 0},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key\s*[:=]\s*["']?([A-Za-z0-9/+=]{30,})`), 1},
	{"assigned-secret", regexp.MustCompile(`(?i)\b([a-z0-9_\-]*(?:api[_-]?key|secret|passwd|password|token|access[_-]?key)[a-z0-9_\-]*)\s*[:=]\s*["']?([^\s"',;]{8,})`), 2},
}

// RedactSecrets replaces secret-shaped values in command output with a marker
// and returns the redacted text plus the set of pattern names that matched.
// With redaction disabled the text is returned unchanged.
func (g *ShellGuard) RedactSecrets(output string) (string, []string) {
	if g == nil || !g.RedactSecretOutput || output == "" {
		return output, nil
	}

	redacted := output
	var hits []string
	for _, p := range secretPatterns {
		if !p.re.MatchString(redacted) {
			continue
		}
		// Replace only the capture group so the surrounding key name stays
		// visible and the model can still tell that a credential was present.
		redacted = p.re.ReplaceAllStringFunc(redacted, func(match string) string {
			idx := p.re.FindStringSubmatchIndex(match)
			if idx == nil {
				return match
			}
			if p.group == 0 {
				return redactedMarker
			}
			start, end := idx[2*p.group], idx[2*p.group+1]
			if start < 0 || end < 0 {
				return match
			}
			return match[:start] + redactedMarker + match[end:]
		})
		hits = append(hits, p.name)
	}

	return redacted, hits
}

// FlagSensitivePaths returns the sensitive paths referenced by a shell command.
// This is advisory: it powers the approval preview, it does not block
// execution, because a shell string cannot be reliably confined.
func (g *ShellGuard) FlagSensitivePaths(command string) []string {
	if g == nil || g.SensitiveMatcher == nil || command == "" {
		return nil
	}

	var flagged []string
	seen := make(map[string]bool)

	for _, token := range shellTokens(command) {
		// Strip redirection and quoting noise so the matcher sees a clean path.
		cleaned := strings.Trim(token, "\"'`()<>|&;,*")
		cleaned = strings.TrimRight(cleaned, ".:")
		if cleaned == "" || strings.HasPrefix(cleaned, "-") {
			continue
		}

		// Check both the bare token and its basename so "secrets/.env" and
		// ".env" are both recognised.
		candidates := []string{cleaned, filepath.Base(cleaned)}
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if pattern := g.SensitiveMatcher.IsSensitive(c); pattern != "" {
				key := c + "|" + pattern
				if !seen[key] {
					seen[key] = true
					flagged = append(flagged, c)
				}
				break
			}
		}
	}

	return flagged
}

// shellTokens splits a command line into candidate path tokens.
func shellTokens(command string) []string {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '|', '&', ';', '<', '>', '(', ')', '\'', '"', '`', '$', '{', '}', '[', ']', '*', '?', '!', '#', '~', '=':
			return true
		}
		return false
	})
	return fields
}

// DescribeFlags renders the advisory warnings shown in the approval preview.
func DescribeFlags(flagged []string) string {
	if len(flagged) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n⚠ WARNING: this command references sensitive paths:")
	for _, f := range flagged {
		b.WriteString("\n  - " + f)
	}
	b.WriteString("\n  The shell is not confined to the workspace. Reading these")
	b.WriteString(" files will expose their contents to the model and the session log.")
	return b.String()
}

// DescribeRedactions renders a note listing which secret patterns were masked.
func DescribeRedactions(hits []string) string {
	if len(hits) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n🔒 %d secret value(s) redacted from output (%s).",
		len(hits), strings.Join(hits, ", "))
}
