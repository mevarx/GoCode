package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Danger classification. ClassifyDanger
// is a PURE function of (tool name, raw args, workspace root): it never
// executes anything, never touches gate state, and only reports whether a
// call looks destructive plus a human-readable reason. The gate wires the
// result into ApprovalRequest.Danger (normal mode warns) and the yolo refusal
// path (dangerous calls are refused without asking).
//
// The rule set is a DOCUMENTED, BOUNDED heuristic — not a shell parser. It is
// deliberately conservative (false positives are acceptable; false negatives
// are the bug direction). ShellGuard, the deny-list and redaction remain
// unconditional at execution time regardless of what this classifier says.
//
// Shell rules (case-insensitive, token/word-boundary aware — "sudoers" is not
// sudo, "farm" is not rm, "git format-patch" is not format):
//
// 1. sudo / doas anywhere in the command (privileged execution).
// 2. rm with BOTH recursive and force flags (-rf, -fr, -Rf, -r -f,
// --recursive --force, PowerShell-style -Recurse -Force).
// 3. mkfs / mkfs.* (filesystem creation).
// 4. dd with an of= operand (raw device/file overwrite).
// 5. shutdown / reboot / poweroff / halt.
// 6. format (Windows volume format; the token "format-patch" does not match).
// 7. del with /s (Windows recursive delete).
// 8. Remove-Item (or alias ri) with BOTH -Recurse and -Force.
// 9. chmod recursively setting mode 777 on / or /* (world-writable root).
// 10. curl/wget in an earlier pipe segment feeding sh/bash/zsh/dash/ksh/
// python/python3/perl/ruby in a later segment (download piped to an
// interpreter; sudo before the interpreter is skipped over).
// 11. > or >> redirecting into /dev/sd*, /dev/hd*, /dev/nvme*, /dev/disk*
// (raw device write; /dev/null is NOT a device write).
// 12. git push with --force/-f/--force-with-lease (or a +refspec) targeting a
// protected branch: main, master, prod, production, stable, release,
// release/*, HEAD. A force push with NO explicit refspec is also flagged
// (the current branch is unknown and may be protected).
//
// Path rule: any non-shell tool whose args carry "path" (or "file_path")
// targeting a location OUTSIDE the workspace root is dangerous. Containment
// is checked lexically and, best-effort, through the nearest existing
// ancestor's symlinks (same helper the file tools use).
//
// The configurable per-tool danger list ([permissions] dangerous_tools) is
// applied by the gate on top of this function, keeping the classifier pure.

// shellToolNames are the tool names whose "command" argument is classified
// with the shell rules. Covers the built-in shell_exec plus common MCP names.
func isShellTool(name string) bool {
	switch name {
	case "shell_exec", "bash", "shell":
		return true
	}
	return false
}

// ClassifyDanger reports whether a tool invocation looks destructive and why.
// workspaceRoot may be empty (the process working directory is used as the
// containment base then). Invalid or missing args are never dangerous by
// themselves — execution-time validation still applies.
func ClassifyDanger(toolName string, args json.RawMessage, workspaceRoot string) (bool, string) {
	if isShellTool(toolName) {
		if cmd := shellCommandArg(args); cmd != "" {
			return classifyShellCommand(cmd)
		}
	}
	return classifyPathDanger(args, workspaceRoot)
}

// shellCommandArg extracts the "command" argument from raw tool args.
func shellCommandArg(args json.RawMessage) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return parsed.Command
}

// argsPath extracts the target path from raw tool args ("path" wins over
// "file_path"). Empty when the tool takes no path.
func argsPath(args json.RawMessage) string {
	var parsed struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	if parsed.Path != "" {
		return parsed.Path
	}
	return parsed.FilePath
}

// classifyPathDanger flags paths outside the workspace root. Shell tools are
// handled by the command rules; every other path-bearing tool (built-in file
// tools and MCP tools alike) is checked here.
func classifyPathDanger(args json.RawMessage, workspaceRoot string) (bool, string) {
	raw := argsPath(args)
	if raw == "" {
		return false, ""
	}
	base := workspaceRoot
	if base == "" {
		if wd, err := os.Getwd(); err == nil {
			base = wd
		}
	}
	if base == "" {
		return false, ""
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false, ""
	}
	// Treat both slash styles as separators, mirroring ValidatePath.
	cleaned := strings.ReplaceAll(raw, "\\", string(filepath.Separator))
	var absPath string
	if filepath.IsAbs(cleaned) {
		absPath = filepath.Clean(cleaned)
	} else {
		absPath = filepath.Clean(filepath.Join(absBase, cleaned))
	}
	if pathOutsideRoot(absBase, absPath) {
		return true, "path outside workspace: " + raw
	}
	// Best-effort symlink containment: resolve the nearest existing ancestor
	// of both sides so a symlinked workspace stays comparable. A resolution
	// failure falls back to the lexical answer above.
	resolvedPath, perr := resolveExistingAncestor(absPath)
	resolvedBase, berr := resolveExistingAncestor(absBase)
	if perr == nil && berr == nil && pathOutsideRoot(resolvedBase, resolvedPath) {
		return true, "path resolves outside workspace: " + raw
	}
	return false, ""
}

// pathOutsideRoot reports whether absPath escapes absBase lexically.
func pathOutsideRoot(absBase, absPath string) bool {
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// classifyShellCommand applies the bounded shell rule list. The command is
// split into segments on shell control operators (; && || | & newlines and
// grouping characters), each segment is tokenized on whitespace, and tokens
// are matched case-insensitively by word boundary (whole token or basename),
// never by substring. Quotes surrounding a token are stripped; the splitter
// does not understand quoting semantics, which can only add false positives.
func classifyShellCommand(cmd string) (bool, string) {
	segments := splitShellSegments(cmd)
	for _, seg := range segments {
		if danger, reason := classifyShellSegment(seg); danger {
			return true, reason
		}
	}
	// Pipe rule: a download in an earlier segment feeding an interpreter in
	// a later one (curl … | sh). Checked across segment boundaries.
	for i, seg := range segments {
		if !segmentHasDownloader(seg) {
			continue
		}
		for _, later := range segments[i+1:] {
			if interp := segmentInterpreter(later); interp != "" {
				return true, "download piped to interpreter (" + interp + ")"
			}
		}
	}
	return false, ""
}

// splitShellSegments splits on unquoted-looking control operators. Empty
// segments (from && or ||) are dropped. Backticks and parentheses are split
// too so command substitutions cannot hide a destructive token.
func splitShellSegments(cmd string) [][]string {
	var segments [][]string
	var cur strings.Builder
	flush := func() {
		tokens := tokenizeShellSegment(cur.String())
		if len(tokens) > 0 {
			segments = append(segments, tokens)
		}
		cur.Reset()
	}
	for _, r := range cmd {
		switch r {
		case ';', '&', '|', '\n', '\r', '(', ')', '{', '}', '`':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segments
}

// tokenizeShellSegment lowercases fields and strips one layer of matching
// surrounding quotes so `rm` and "rm" match the same rule.
func tokenizeShellSegment(seg string) []string {
	fields := strings.Fields(seg)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		if len(f) >= 2 {
			if (f[0] == '\'' && f[len(f)-1] == '\'') || (f[0] == '"' && f[len(f)-1] == '"') {
				f = f[1 : len(f)-1]
			}
		}
		if f != "" {
			tokens = append(tokens, f)
		}
	}
	return tokens
}

// commandBase strips a directory prefix and a Windows.exe suffix so
// /usr/bin/rm, C:\tools\rm.exe and rm all match one rule.
func commandBase(token string) string {
	if i := strings.LastIndexAny(token, "/\\"); i >= 0 {
		token = token[i+1:]
	}
	return strings.TrimSuffix(token, ".exe")
}

// segmentHasFlag reports whether any token is exactly flag (long form) or a
// short-flag bundle containing char (e.g. -rf contains r and f).
func segmentHasFlag(tokens []string, long string, short byte) bool {
	for _, t := range tokens {
		if t == long || t == "-"+string(short) {
			return true
		}
		if len(t) >= 2 && t[0] == '-' && t[1] != '-' && strings.IndexByte(t[1:], short) >= 0 {
			return true
		}
	}
	return false
}

func classifyShellSegment(tokens []string) (bool, string) {
	if len(tokens) == 0 {
		return false, ""
	}

	// 1. Privileged execution: sudo/doas as a whole token anywhere.
	for _, t := range tokens {
		if b := commandBase(t); b == "sudo" || b == "doas" {
			return true, "privileged execution (" + b + ")"
		}
	}

	for i, t := range tokens {
		base := commandBase(t)
		switch {
		// 2. rm with recursive AND force.
		case base == "rm":
			rest := tokens[i:]
			if segmentHasFlag(rest, "--recursive", 'r') && segmentHasFlag(rest, "--force", 'f') {
				return true, "recursive forced delete (rm -rf)"
			}
			// 3. mkfs / mkfs.*
		case base == "mkfs" || strings.HasPrefix(base, "mkfs."):
			return true, "filesystem creation (" + base + ")"
			// 4. dd with an of= operand.
		case base == "dd":
			for _, op := range tokens[i:] {
				if strings.HasPrefix(op, "of=") {
					return true, "raw overwrite (dd of=)"
				}
			}
			// 5. Power state.
		case base == "shutdown" || base == "reboot" || base == "poweroff" || base == "halt":
			return true, "power state change (" + base + ")"
			// 6. Windows volume format.
		case base == "format":
			return true, "volume format"
			// 7. Windows recursive delete.
		case base == "del":
			for _, op := range tokens[i:] {
				if op == "/s" {
					return true, "recursive delete (del /s)"
				}
			}
			// 8. PowerShell Remove-Item with -Recurse -Force.
		case base == "remove-item" || base == "ri":
			rest := tokens[i:]
			if segmentHasFlag(rest, "-recurse", 0) && segmentHasFlag(rest, "-force", 0) {
				return true, "recursive forced delete (Remove-Item -Recurse -Force)"
			}
			// 9. World-writable root.
		case base == "chmod":
			rest := tokens[i:]
			if segmentHasFlag(rest, "--recursive", 'r') && segmentHasToken(rest, "777") &&
				(segmentHasToken(rest, "/") || segmentHasToken(rest, "/*")) {
				return true, "world-writable root (chmod -R 777 /)"
			}
			// 12. Force push to a protected branch.
		case base == "git":
			if danger, reason := classifyGitPush(tokens[i:]); danger {
				return true, reason
			}
		}
	}

	// 11. Redirect into a raw device (also handles the glued >/dev/sda form).
	for i, t := range tokens {
		target := ""
		switch {
		case (t == ">" || t == ">>") && i+1 < len(tokens):
			target = tokens[i+1]
		case strings.HasPrefix(t, ">>"):
			target = t[2:]
		case strings.HasPrefix(t, ">"):
			target = t[1:]
		}
		if isRawDevicePath(target) {
			return true, "write to raw device (" + target + ")"
		}
	}
	return false, ""
}

func segmentHasToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// isRawDevicePath matches the bounded device-prefix list. /dev/null and other
// pseudo-devices are deliberately NOT raw devices.
func isRawDevicePath(p string) bool {
	for _, prefix := range []string{"/dev/sd", "/dev/hd", "/dev/nvme", "/dev/disk"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// protectedBranches is the bounded force-push protection list. "release" also
// covers release/* branches; HEAD stands for the (unknown) current branch.
func isProtectedBranch(ref string) bool {
	ref = strings.TrimPrefix(ref, "+")
	ref = strings.TrimPrefix(ref, "refs/heads/")
	switch ref {
	case "main", "master", "prod", "production", "stable", "release", "head":
		return true
	}
	return strings.HasPrefix(ref, "release/")
}

// classifyGitPush inspects the tokens from "git" onward. A force flag
// (--force, -f, --force-with-lease) or a +refspec makes the push forced; the
// push is dangerous when any named refspec is protected, or when no refspec
// is named at all (the current branch may be protected).
func classifyGitPush(tokens []string) (bool, string) {
	pushIdx := -1
	for i := 1; i < len(tokens); i++ {
		if tokens[i] == "push" {
			pushIdx = i
			break
		}
	}
	if pushIdx < 0 {
		return false, ""
	}
	rest := tokens[pushIdx+1:]
	forced := false
	var refspecs []string
	for _, t := range rest {
		switch t {
		case "--force", "-f", "--force-with-lease":
			forced = true
			continue
		}
		if strings.HasPrefix(t, "-") {
			continue
		}
		if strings.HasPrefix(t, "+") {
			forced = true
		}
		refspecs = append(refspecs, t)
	}
	if !forced {
		return false, ""
	}
	// refspecs[0] is usually the remote; every named token is checked so a
	// protected branch is caught regardless of position.
	for _, ref := range refspecs {
		if isProtectedBranch(ref) {
			return true, "force push to protected branch (" + strings.TrimPrefix(ref, "+") + ")"
		}
	}
	if len(refspecs) == 0 {
		return true, "force push with no explicit branch (current branch may be protected)"
	}
	return false, ""
}

// segmentHasDownloader reports a curl/wget token in the segment.
func segmentHasDownloader(tokens []string) bool {
	for _, t := range tokens {
		if b := commandBase(t); b == "curl" || b == "wget" {
			return true
		}
	}
	return false
}

// segmentInterpreter returns the interpreter named by the segment's first
// meaningful token (sudo/doas prefixes are skipped), or "".
func segmentInterpreter(tokens []string) string {
	interpreters := map[string]bool{
		"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
		"python": true, "python3": true, "perl": true, "ruby": true,
	}
	for _, t := range tokens {
		b := commandBase(t)
		if b == "sudo" || b == "doas" {
			continue
		}
		if interpreters[b] {
			return b
		}
		return ""
	}
	return ""
}
