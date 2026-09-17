package ignore

import (
	"path/filepath"
	"strings"
)

// defaultSensitivePatterns are built-in patterns for files that should never be
// exposed to the AI agent by default. These are checked by basename and by glob.
var defaultSensitivePatterns = []string{
	// Environment files
	".env",
	".env.local",
	".env.development",
	".env.production",
	".env.staging",
	".env.test",

	// SSH keys
	"id_rsa",
	"id_rsa.pub",
	"id_ed25519",
	"id_ed25519.pub",
	"id_ecdsa",
	"id_dsa",

	// TLS/SSL
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"*.jks",
	"*.keystore",

	// Cloud credentials
	"credentials.json",
	"service-account.json",
	"service_account.json",

	// Token / secret files
	"*.secret",
	".npmrc",
	".pypirc",
	".netrc",
	".htpasswd",
	".pgpass",
	"token.json",

	// Docker secrets
	".docker/config.json",

	// AWS
	"aws_credentials",
	".aws/credentials",
	".aws/config",

	// GCP
	"gcloud/credentials.db",
	"application_default_credentials.json",

	// Azure
	".azure/accessTokens.json",

	// Kubernetes
	"kubeconfig",
	".kube/config",
}

// SensitiveMatcher wraps an existing Matcher and additionally checks against
// built-in sensitive file patterns.
type SensitiveMatcher struct {
	Inner    Matcher
	Patterns []string
}

// NewSensitiveMatcher creates a matcher that combines the given inner matcher
// with built-in sensitive file patterns. Additional patterns can be added.
func NewSensitiveMatcher(inner Matcher, extraPatterns []string) *SensitiveMatcher {
	patterns := make([]string, len(defaultSensitivePatterns))
	copy(patterns, defaultSensitivePatterns)
	patterns = append(patterns, extraPatterns...)
	if inner == nil {
		inner = &noopMatcher{}
	}
	return &SensitiveMatcher{
		Inner:    inner,
		Patterns: patterns,
	}
}

// IsIgnored checks both the inner matcher and the sensitive file patterns.
func (s *SensitiveMatcher) IsIgnored(path string, isDir bool) bool {
	if s.Inner != nil && s.Inner.IsIgnored(path, isDir) {
		return true
	}
	return false
}

// IsSensitive checks if the given path matches any sensitive file pattern.
// Returns the matching pattern if sensitive, or empty string if not.
func (s *SensitiveMatcher) IsSensitive(path string) string {
	base := filepath.Base(path)
	normalizedPath := filepath.ToSlash(path)

	for _, pattern := range s.Patterns {
		// Check exact basename match
		if base == pattern {
			return pattern
		}

		// Check .env.* style patterns
		if pattern == ".env" && base == ".env" {
			return pattern
		}
		if strings.HasPrefix(pattern, ".env.") && strings.HasPrefix(base, ".env.") {
			return pattern
		}

		// Check glob patterns against basename
		if strings.ContainsAny(pattern, "*?[") {
			if matched, _ := filepath.Match(pattern, base); matched {
				return pattern
			}
		}

		// Check path suffix match (e.g., ".aws/credentials")
		if strings.Contains(pattern, "/") {
			if strings.HasSuffix(normalizedPath, pattern) {
				return pattern
			}
		}
	}

	// Special case: any file starting with ".env."
	if strings.HasPrefix(base, ".env.") {
		return ".env.*"
	}

	return ""
}

// ShouldBlock combines IsIgnored and IsSensitive checks.
// Returns (blocked bool, reason string).
func (s *SensitiveMatcher) ShouldBlock(path string, isDir bool) (bool, string) {
	if s.IsIgnored(path, isDir) {
		return true, "file is ignored by ignore rules (.gocodeignore/.gitignore)"
	}
	if pattern := s.IsSensitive(path); pattern != "" {
		return true, "access denied: matches sensitive file pattern \"" + pattern + "\""
	}
	return false, ""
}
