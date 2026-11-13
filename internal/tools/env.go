package tools

import (
	"os"
	"path"
	"strings"
)

// essentialEnvVars are never scrubbed: child processes need them to run at all.
// Matching is case-insensitive; LC_* is handled separately.
var essentialEnvVars = map[string]bool{
	"PATH":         true,
	"HOME":         true,
	"USERPROFILE":  true,
	"USERNAME":     true,
	"USER":         true,
	"SHELL":        true,
	"TERM":         true,
	"LANG":         true,
	"TMPDIR":       true,
	"TEMP":         true,
	"TMP":          true,
	"SYSTEMROOT":   true,
	"WINDIR":       true,
	"COMSPEC":      true,
	"PATHEXT":      true,
	"PWD":          true,
	"HOSTNAME":     true,
	"PROGRAMDATA":  true,
	"APPDATA":      true,
	"LOCALAPPDATA": true,
}

// credentialEnvPatterns match credential-shaped variable names. A name
// carrying a provider key under an unrecognized name (MY_GATEWAY_KEY) is
// still dropped.
var credentialEnvPatterns = []string{
	"*_API_KEY",
	"*_TOKEN",
	"*_SECRET",
	"*_PASSWORD",
	"*_PASSWD",
	"*_KEY",
	"*_ACCESS_KEY",
	"*_PRIVATE_KEY",
	"*_CREDENTIALS",
}

// ScrubbedEnv returns os.Environ() minus credential-shaped variables and any
// names or patterns in extra. Essential variables always survive. Child
// processes therefore never inherit provider API keys by default.
func ScrubbedEnv(extra []string) []string {
	return scrubEnv(os.Environ(), extra)
}

// scrubEnv is the pure core of ScrubbedEnv so tests can supply an environment.
func scrubEnv(environ []string, extra []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		eq := strings.IndexByte(entry, '=')
		if eq < 0 {
			// Malformed entries are passed through untouched.
			kept = append(kept, entry)
			continue
		}
		if shouldScrubEnvKey(entry[:eq], extra) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func shouldScrubEnvKey(key string, extra []string) bool {
	upper := strings.ToUpper(key)
	if essentialEnvVars[upper] || strings.HasPrefix(upper, "LC_") {
		return false
	}

	for _, pattern := range extra {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.Contains(pattern, "*") {
			if matched, _ := path.Match(strings.ToUpper(pattern), upper); matched {
				return true
			}
			continue
		}
		if strings.EqualFold(pattern, key) {
			return true
		}
	}

	for _, pattern := range credentialEnvPatterns {
		if matched, _ := path.Match(pattern, upper); matched {
			return true
		}
	}
	return false
}
