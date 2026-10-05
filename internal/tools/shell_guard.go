package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

// ShellGuard redacts secrets from shell output and warns about sensitive paths. It does not sandbox the shell.
// SECURITY: file tools stay confined via ValidatePath; the shell is advisory-only.
type ShellGuard struct {
	// SensitiveMatcher flags sensitive paths. May be nil; redaction still applies.
	SensitiveMatcher *ignore.SensitiveMatcher
	// RedactSecretOutput enables redaction of secret-shaped values in output.
	RedactSecretOutput bool
}

const redactedMarker = "***REDACTED***"

// secretPatterns match credential-shaped values; order affects reporting only.
var secretPatterns = []struct {
	name  string
	re    *regexp.Regexp
	group int
}{
	{"anthropic-api-key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`), 0},
	{"openai-api-key", regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`), 0},
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`), 0},
	{"github-fine-grained-pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`), 0},
	{"google-api-key", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`), 0},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`), 0},
	{"slack-app-token", regexp.MustCompile(`xapp-[A-Za-z0-9\-]{10,}`), 0},
	{"aws-access-key-id", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"stripe-key", regexp.MustCompile(`(?:sk|rk)_live_[A-Za-z0-9]{16,}`), 0},
	{"sendgrid-key", regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{16,}`), 0},
	{"npm-token", regexp.MustCompile(`npm_[A-Za-z0-9]{20,}`), 0},
	{"pypi-token", regexp.MustCompile(`pypi-[A-Za-z0-9_\-]{16,}`), 0},
	{"digitalocean-token", regexp.MustCompile(`dop_v1_[A-Za-z0-9_\-]{16,}`), 0},
	{"google-oauth-access-token", regexp.MustCompile(`ya29\.[A-Za-z0-9_\-]+`), 0},
	{"private-key-block", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), 0},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key\s*[:=]\s*["']?([A-Za-z0-9/+=]{30,})`), 1},
	// JSON-tolerant: covers {"apiKey": "..."} as well as api_key: ...
	{"assigned-secret", regexp.MustCompile(`(?i)\b([a-z0-9_\-]*(?:api[_-]?key|secret|passwd|password|token|access[_-]?key)[a-z0-9_\-]*)["']?\s*[:=]\s*["']?([^\s"',;]{8,})`), 2},
}

// RedactToolOutput masks credential-shaped values unconditionally. SECURITY: it is the transcript boundary.
func RedactToolOutput(output string) (string, []string) {
	if output == "" {
		return output, nil
	}
	redacted := output
	var hits []string
	for _, p := range secretPatterns {
		if !p.re.MatchString(redacted) {
			continue
		}
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

// RedactSecrets masks secret-shaped values. With redaction disabled the text is unchanged.
func (g *ShellGuard) RedactSecrets(output string) (string, []string) {
	if g == nil || !g.RedactSecretOutput || output == "" {
		return output, nil
	}
	return RedactToolOutput(output)
}

// FlagSensitivePaths returns sensitive paths referenced by a command for the approval preview. Advisory only.
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

		// Match bare token and basename so "secrets/.env" and ".env" are both recognised.
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
