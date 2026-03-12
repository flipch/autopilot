# Changelog

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
