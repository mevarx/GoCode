# Security notes

This file records dependency-related security decisions that reviewers and future
maintainers must not silently reverse. It documents risks that are **accepted**, not
risks that are fixed.

## Vulnerability tracking

`govulncheck` is the tracking mechanism for the advisories referenced below. CI runs
`govulncheck ./...` on every push and pull request; findings are triaged against this
document. If that CI job is missing or red, treat the dependency posture as
unverified and run it locally before a release:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

A finding is only actionable if it is *reachable* from GoCode code. Unreachable
advisories are recorded here as accepted, with the reason.

## Known accepted risks / dependencies

### `github.com/ollama/ollama` (local Ollama provider)

GoCode imports only two packages of this module: `api` (HTTP client) and
`envconfig` (`Host()` host/port resolution). The Ollama server daemon, model
runtimes, and GGUF loading code are never built or executed by GoCode.

Accepted because the module is required to speak the Ollama chat API. Advisories in
the parts of the module GoCode does not link are out of scope; advisories reachable
through `api`/`envconfig` are in scope and are fixed by bumping the module version.

Mitigations that bound the risk while an upgrade is pending:

- The provider never uses `http.DefaultClient`. It builds its own `http.Client`
  with a 10-minute whole-turn timeout, a 30s dial timeout, a 15s TLS handshake
  timeout, and `NewRetryTransport` (`internal/provider/retry.go`).
- The retry transport replays idempotent methods only, so a `/api/chat` POST is
  never duplicated or re-billed.
- `OLLAMA_HOST` is resolved by the upstream `envconfig.Host()` rather than a local
  parser, so host/port normalization stays owned by the dependency instead of
  drifting.
- Truncation is propagated (`done_reason` → `StreamChunk.FinishReason`) instead of
  being dropped, so a token-limit stop is visible to the agent loop and the user.

Upgrade policy: bump the module when govulncheck reports a reachable advisory in
`api` or `envconfig`; record any accepted unreachable advisory in this section with
its CVE/GO-ID and the reason it is unreachable.

### `github.com/go-git/go-git/v5` (gitignore parsing only) — accepted, not removed

Only `internal/ignore/ignore.go` imports go-git:
`github.com/go-git/go-git/v5/plumbing/format/gitignore` (`ParsePattern`,
`NewMatcher`). No other package imports go-git, and no go-git plumbing, storage, or
transport code is reachable from GoCode.

Removing the dependency (local matcher or `github.com/sabhiram/go-gitignore`
behind the existing `ignore.Matcher` interface) was evaluated and **rejected** for
now, because exact pattern parity could not be guaranteed. The behaviors that a
replacement must reproduce are gitignore-engine details, not obvious rules:

- Non-slash patterns are matched against **any** path segment; the `dirOnly` flag is
  only enforced on the final segment. This is why `secrets/` ignores
  `secrets/key.txt` and why `build/` matches when `isDir` is true.
- Patterns containing `/` switch to a glob matcher with `**` support; a leading `/`
  becomes an empty first segment, so root anchoring is produced by the engine.
- Segment globbing delegates to `filepath.Match`, which has OS-specific path
  separator and escape behavior on Windows — the ignore tests run on Windows and
  macOS in CI.
- Precedence is last-match-wins over the reversed pattern list, so negation
  (`!pattern`) only wins when it appears after the pattern it overrides.
- Case-insensitivity is implemented by lowercasing both the patterns and the path
  segments before matching (`IsCaseInsensitiveFS()`), and `.gocodeignore` takes
  precedence over `.gitignore` in GoCode, not in go-git.

`github.com/sabhiram/go-gitignore` compiles patterns into a single ordered regex
list; its negation and directory-only handling differ from the segment/`filepath.Match`
model above, so swapping it would change behavior for anchored and negated
patterns even where the existing tests pass by luck. A local reimplementation would
have to carry the same quirks to be a true drop-in.

Accepted cost: go-git's transitive dependency set (`gcfg`, `billy`, and friends)
stays in `go.mod`/`go.sum` and is scanned by govulncheck. Re-evaluate removal when a
reachable advisory is reported for `plumbing/format/gitignore`, or when a parity
harness covering anchored, negated, dir-only, case-folded, and
`.gocodeignore`-precedence cases exists and passes on Linux, macOS, and Windows.
Until then the dependency stays and this status is the recorded rationale.

## Hardlink identity checks and their cache

`SensitiveIdentitySet` (`internal/tools/workspace.go`) collects the `FileInfo` of
every sensitive file in the workspace so `file_read`, `file_write`, `file_patch`,
and `code_search` can refuse hardlink aliases by inode identity (`os.SameFile`)
rather than by name. Walking the whole workspace on every tool call is O(workspace),
so the result is cached per normalized workspace root **and** sensitive matcher for
30 seconds (`sensitiveIdentityCacheTTL`). The returned slice is shared between
callers and must be treated as read-only; callers must never mutate or append to it.

Cache freshness:

- A successful `file_write` or `file_patch` calls
  `InvalidateSensitiveIdentityCache(root)` immediately after the bytes land on disk,
  so approvals and later checks in the same turn never see pre-write identities.
  Previews and refused/failed writes do not invalidate anything.
- The TTL only bounds drift from changes GoCode did not make (external processes
  creating or removing hardlinks). Invalidating one root never evicts another root.

The in-workspace walk cannot see the *original* path of a hardlink that lives
outside the workspace: `ln ~/.ssh/id_rsa notes.txt` creates an in-workspace name
whose only other links are elsewhere. To cover the common credential files, the
identity set also stats a fixed list of external paths relative to the user's home
directory (`~/.ssh/id_rsa`, `~/.ssh/id_ed25519`, `~/.aws/credentials`,
`~/.kube/config`, `~/.netrc`, `~/.npmrc`) and includes their identities. Their
`FileInfo` is only used for `SameFile` comparison; no content is read and no
in-workspace file is blocked unless it actually shares the inode.

Known remaining gap (accepted): a hardlink to an external sensitive file **not** in
that fixed list is not detected by the walk. Path confinement (`ValidatePath`) still
stops GoCode from opening a path outside the workspace by name, but a hardlink
created inside the workspace is a legitimate in-workspace path and can expose the
external file's content. Closing this generally requires kernel-enforced path
resolution that rejects hardlink aliases, e.g. `openat2(RESOLVE_BENEATH)` on Linux
or `O_NOFOLLOW` plus identity verification at open time; until then the fixed
external probe list plus the 30-second cache bound the exposure.

## MCP limitations

MCP servers are untrusted child processes, and the integration deliberately stops
short of a full supervisor. Known limitations, accepted for now:

- **No push notifications.** `notifications/tools/list_changed` is ignored. A
  server whose tool list changes after startup keeps its old registrations; picking
  up new or removed tools requires restarting GoCode (or re-adding the server).
- **Server processes are scrubbed-env children.** They are spawned through
  `tools.ScrubbedEnv` plus the configured `env` map, so credential-shaped
  variables are absent, but the server still runs with the user's privileges and
  full filesystem access. Only configure servers you trust.
- **Dead servers are restarted on demand.** When a server process exits, its tools
  stay registered; the next call to one of them restarts and re-initializes the
  server and retries exactly once. A failed restart surfaces as an
  `MCP server "<name>" unavailable: ...` tool error, and after `CloseAll` no
  further restarts are attempted. Restarts are serialized with shutdown, so a
  restart cannot resurrect a process after GoCode begins closing servers.
- **Advertised metadata is capped.** A server advertising more than 200 tools is
  rejected outright; individual tools with a name over 128 bytes, a description
  over 8 KiB, or an input schema over 32 KiB are skipped (and reported) rather than
  truncated. This bounds context flooding from a hostile or broken server.
