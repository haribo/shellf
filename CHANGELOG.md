# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-08-03

First tagged release. shellf provisions targets over SSH via a detached resident
agent that evaluates on the host — "raw shell, but idempotent, previewable, fast."

### Added

- Language: lexer, parser, and evaluator with the `observe`/`apply`/`status` def
  contract, `if`/`else`, error handling (`?`), parse-time `for` loops, and
  `with { }` per-call variable overrides.
- Idempotent execution with a `--check` preview and a `status` mode that reports
  current-vs-desired state per host.
- Templating: `template(src, dst)` with the `@{var}` delimiter, rendered per host
  over inventory variables.
- Secrets: `--secret-file` / `--secret-env` with value-based redaction in all
  output, and a tmpfs workdir that keeps plaintext off the target's persistent disk.
- Modules: directory-package user defs, local imports, and hash-verified remote git
  modules with `shellf.lock` and a content-addressed cache.
- Embedded, self-hosted standard library: apt, docker/compose, systemd, ufw, files,
  users, git-sync, http-check, and more.
- SSH transport with a resident agent (inactivity TTL), host-key verification, and
  per-user agent/workdir scoping.
- Commands: `run`, `status`, `clean`, and `version`.

[Unreleased]: https://github.com/haribo/shellf/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/haribo/shellf/releases/tag/v0.1.0
