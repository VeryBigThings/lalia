# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-04-20

### Added
- New `peer` role for decentralized coordination without supervisor/worker constraints.
- Dedicated `prompts/peer.md` for peer agents.
- Support for `peer` role in `init`, `prompt`, `run`, and `register` commands.
- Shell completions for the `peer` role.
- `VERSION` file for tracking the current semver.
- `CHANGELOG.md` for historical release tracking.

### Changed
- **Breaking Change**: Renamed the project from `kopos` to `lalia` throughout the codebase, build system, and documentation.
- **Breaking Change**: Updated Go module path to `github.com/neektza/lalia`.
- Updated environment variables to use `LALIA_` prefix (e.g., `LALIA_HOME`, `LALIA_NAME`).
- Binary name is now `lalia`.

### Removed
- Old `kopos` binary and shell completion files from installation paths.
