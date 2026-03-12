# Changelog

## [0.7.7] - 2026-03-12

### Fixed
- Inject `CI=true` env var for agent subprocesses so rp1's `/pr-review` posts findings as GitHub PR comments (P5 phase requires CI mode)

## [0.7.6] - 2026-03-12

### Fixed
- PR detection now tries multiple branch name patterns (`slug`, `worktree-slug`, `feature-slug`, `worktree-feature-slug`, `quick-build-slug`) to handle different prefixes from rp1 and Claude Code worktree systems

## [0.7.5] - 2026-03-12

### Fixed
- Zellij workers now stagger starts by 5 seconds each to prevent claim races on bd issues
- Worker 1 starts immediately, worker 2 after 5s, worker 3 after 10s, etc.

## [0.7.4] - 2026-03-12

### Fixed
- All `/rp1-build` prompts now include `--git-worktree` for isolated worktree per task — prevents parallel workers from stepping on each other in the same repo directory

## [0.7.3] - 2026-03-12

### Fixed
- Zellij layout now uses one **tab per worker** instead of cramming all panes into a single view
- Use `--new-session-with-layout` for new sessions, `action new-tab --layout` when already inside zellij
- Both paths use the same layout file

## [0.7.2] - 2026-03-12

### Fixed
- Zellij layout file no longer deleted before zellij can read it (was causing "no active session" errors)
- Layout written to `<repo>/.autopilot/zellij-layout.kdl` instead of a temp file that raced with `syscall.Exec`

## [0.7.1] - 2026-03-12

### Fixed
- Workers no longer hot-loop on already-claimed issues — they now iterate through the ready list and skip claimed ones
- 5-second backoff when all ready issues are taken by other workers

## [0.7.0] - 2026-03-12

### Added
- `--effort` flag (default `max`) for controlling Claude thinking effort level (low, medium, high, max)
- Per-role model/effort overrides via config file `roles` section (builder, reviewer, fixer)
- `detectVerdict` function — checks GitHub PR review decision first, falls back to file parsing
- Injects `RP1_PR_REVIEW_VERDICT=auto` and `RP1_PR_REVIEW_ADD_COMMENTS=true` env vars so `/pr-review` posts a real GitHub review for reliable verdict detection

### Changed
- Review verdict detection no longer depends solely on parsing natural language from review files

## [0.6.1] - 2026-03-12

### Changed
- Claude launcher now includes `--effort max` for maximum extended thinking in autonomous sessions

## [0.6.0] - 2026-03-12

### Added
- `--zellij` flag on `loop` command — spawns each worker in its own zellij pane for full visibility and interaction
- If already in a zellij session: adds panes via `zellij action new-pane`
- If not: generates a KDL layout and launches a new zellij session (`autopilot-<repo>`)
- Each pane runs a single-worker `autopilot loop --parallel 1` with full terminal I/O
- CLAUDECODE env vars stripped from zellij session environment

## [0.5.0] - 2026-03-12

### Added
- `--parallel N` flag on `loop` command — spawns N concurrent workers, each picking and processing issues independently
- Auto-detect mode (`--parallel 0`, default) — counts ready issues and spawns that many workers, capped at 5
- Worker log prefixes (`[w1]`, `[w2]`, ...) for distinguishing parallel worker output
- Graceful claim-race handling — if two workers try to claim the same issue, the loser retries with the next one
- `stopCh` broadcast for coordinated shutdown across all workers

### Changed
- Refactored `runLoop` into `runLoop` (orchestrator) + `runWorker` (per-issue loop)
- Parallel workers discard agent stdout/stderr — autopilot log (`--log-file`) is the single source of truth

## [0.4.0] - 2026-03-12

### Added
- `--log-file` flag on `loop` command — writes structured logs to a dedicated file in addition to stderr, so an orchestrator can `tail -f` progress without parsing agent noise
- Automatic `CLAUDECODE` and `CLAUDE_CODE` env var stripping when launching agent subprocesses — fixes nested session errors when autopilot is invoked from inside a Claude Code session

### Fixed
- Agents launched by autopilot no longer fail with "already inside a Claude session" when autopilot itself runs inside Claude Code

## [0.3.1] - 2026-03-11

### Fixed
- Claude launcher now includes `--dangerously-skip-permissions` so agent sessions run unattended

## [0.3.0] - 2026-03-11

### Added
- `--review` flag on `autopilot loop` for full PR review cycle
- `--max-review-rounds` flag (default 3) to cap review/fix iterations
- Autopilot-driven PR lifecycle: detect PR via `gh`, launch review agent (`/pr-review`), parse verdict from `.rp1/work/pr-reviews/`, launch fix agent (`/address-pr-feedback`), push fixes, merge via `gh pr merge --squash`, close beads issue
- `extractVerdict` parser for rp1 pr-review report files (emoji and structured patterns)
- `launchAgent`, `detectPR`, `mergePR` helper functions
- `gh` binary validation when `--review` is enabled

### Changed
- `buildRP1Prompt` now accepts `gitPR` parameter — `--git-pr` is included only when `--review` is active
- Without `--review`, loop behavior is unchanged (no PR, close on agent success)

## [0.2.0] - 2026-03-11

### Added
- `autopilot loop` command for continuous processing of all ready Beads issues
- Proper logging with timestamps to stderr for loop operations
- `--cooldown` flag to control pause between tasks (default 10s)
- `--max-tasks` flag to limit number of issues processed per loop run
- Graceful shutdown on SIGINT/SIGTERM during loop
- Automatic `bd close` on successful agent completion

### Changed
- Removed `--git-pr` from generated `/rp1-build` prompts without `--review`

## [0.1.1] - 2026-03-11

### Fixed
- corrected the module path to `github.com/flipch/autopilot`
- switched the installer to authenticated `gh release download` for the private repo
- updated install documentation for private repository usage

## [0.1.0] - 2026-03-11

### Added
- `autopilot next` to pick the next ready Beads task and seed `/rp1-build`
- support for OpenCode and Claude launchers
- optional config file support from `~/.config/autopilot/config.json`
- `--print-prompt` for inspecting the generated build prompt without launching
- install and release automation for published Go binaries
