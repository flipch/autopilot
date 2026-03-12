# Changelog

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
