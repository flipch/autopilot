# autopilot

Small Go CLI for kicking off the next ready Beads task through OpenCode or Claude Code and `/rp1-build`.

## What it does

- runs `bd ready --json` in a target repo
- selects the next best ready issue
- optionally claims it
- builds an `/rp1-build "requirement" "description" --afk` prompt
- launches OpenCode in the target repo with `opencoder` and an Opus-class model
- can fall back to Claude Code with `--launcher claude`
- **loop mode**: continuously processes all ready issues, closing each on success

## Install

```bash
GOPRIVATE=github.com/flipch/* go install github.com/flipch/autopilot/cmd/autopilot@latest
```

For published binaries from this private repo, use the authenticated installer:

```bash
gh repo clone flipch/autopilot && ./autopilot/scripts/install.sh
```

To pin a version:

```bash
AUTOPILOT_VERSION=v0.1.1 ./autopilot/scripts/install.sh
```

## Usage

```bash
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber
```

### Common options

```bash
# Preview without claiming or launching
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --dry-run

# Pick from ready items interactively
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --pick

# Use a specific issue
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --issue jobber-t6m.7

# Skip claim
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --no-claim

# Launch with Claude Code instead of OpenCode
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --launcher claude

# Print just the generated /rp1-build prompt
go run ./cmd/autopilot next --repo /Users/felipeh/Development/jobber --print-prompt
```

### Loop mode

Process all ready issues sequentially, closing each on success:

```bash
# Loop through all ready issues with Claude
autopilot loop --repo /Users/felipeh/Development/jobber --launcher claude

# Limit to 5 tasks with a 30s cooldown between each
autopilot loop --repo /Users/felipeh/Development/jobber --max-tasks 5 --cooldown 30s

# Use OpenCode (default launcher)
autopilot loop --repo /Users/felipeh/Development/jobber
```

The loop:
1. Picks the highest-priority ready issue
2. Claims it
3. Launches the agent
4. On exit 0: closes the issue via `bd close`
5. On non-zero exit: logs the failure, continues to next issue
6. Repeats until no ready issues remain (or `--max-tasks` reached)
7. Handles SIGINT/SIGTERM gracefully

### Loop with PR review

Add `--review` to enable the full PR lifecycle. Autopilot orchestrates everything — agents are only invoked for AI-heavy work (review analysis and code fixes):

```bash
autopilot loop --repo ~/Development/jobber --launcher claude --review
autopilot loop --repo ~/Development/jobber --review --max-review-rounds 5
```

With `--review` enabled, the loop becomes:
1. Build agent runs with `--git-pr --afk` → creates PR
2. **Autopilot** detects the PR via `gh pr list --head <branch>`
3. **Autopilot** launches a review agent with `/pr-review <pr-number>`
4. **Autopilot** parses the verdict from `.rp1/work/pr-reviews/`
5. If approved → **autopilot** merges via `gh pr merge --squash` → closes beads issue
6. If changes requested → **autopilot** launches fix agent with `/address-pr-feedback <pr> --afk` → pushes fixes → re-reviews
7. If blocked or max rounds reached → logs failure, moves to next issue

Requires `gh` CLI authenticated and in PATH.

### Logging

By default, loop logs go to stderr (mixed with agent output). Use `--log-file` for clean, dedicated logs:

```bash
autopilot loop --repo ~/Development/jobber --launcher claude --log-file /tmp/autopilot.log

# Monitor from another terminal:
tail -f /tmp/autopilot.log
```

Logs are written to both stderr and the file, with timestamps and structured messages for each step (claim, launch, review, merge, close).

## Defaults

- launcher: `opencode`
- model: `anthropic/claude-opus-4-6`
- agent: `opencoder`

For Claude launcher, the default model becomes `opus`.

## Config

Autopilot optionally reads:

```bash
~/.config/autopilot/config.json
```

Example:

```json
{
  "repo": "/Users/felipeh/Development/jobber",
  "launcher": "opencode",
  "model": "anthropic/claude-opus-4-6",
  "agent": "opencoder",
  "no_claim": false
}
```

CLI flags override config values.

## Version

```bash
autopilot version
```

## Requirements

- `bd` in `PATH`
- `opencode` or `claude` in `PATH`, depending on launcher
- target repo must be a git repo with Beads configured

## Releases

Push annotated tags matching `v*.*.*` to trigger the GitHub Actions release workflow. The workflow runs `go test ./...` and publishes cross-platform binaries through Goreleaser using `goreleaser.yaml`.
