# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Transparent TCP/UDP destination redirector for Windows, backed by an embedded WinDivert driver (`WinDivert.dll` + `WinDivert64.sys`).
- Single-file binary distribution: drivers extract to a content-hashed `%PROGRAMDATA%\detour\runtime-<hash>\` cache on first run.
- CLI flags: `--from`, `--to`, `--protocol tcp|udp|both`, `-v`, `--version`.
- Forward/reverse WinDivert handles managed under an `errgroup`, with a dedicated shutdown watcher that cleanly aborts both handles on `Ctrl+C` / `SIGTERM`.

[Unreleased]: https://github.com/LeeJeKyun/detour/compare/v0.0.0...HEAD
