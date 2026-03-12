# Changelog

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
