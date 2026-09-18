# ADR-0002: `omatorrent-service` is implemented in Go

STATUS: ACCEPTED (2026-09-18, project bootstrap)

## CONTEXT

The daemon needs: a long-running single-binary service for a Linux/Arch
target, a Unix-domain IPC server, HTTP client work against the qBittorrent
WebUI API with incremental synchronization, SQLite persistence, and a
systemd user service. Candidate ecosystems: Go, Rust, Python, C++/Qt.

## DECISION

`omatorrent-service` is implemented in Go unless explicitly superseded by a
future ADR.

## CONSEQUENCES

- Single static binary, straightforward systemd user unit, strong standard
  library for HTTP/JSON/Unix sockets, mature SQLite drivers, first-class
  concurrency for monitors and sync loops, fast cold start (bar UX).
- Go toolchain becomes a build prerequisite (note: not yet installed on the
  workstation — ROADMAP Phase 0).
- The choice is labeled honestly: it is a pragmatic decision made at
  bootstrap, not a universally proven fact; it can be revisited via ADR
  with implementation evidence.

## ALTERNATIVES

- Rust: equally viable for this scope; not chosen — higher implementation
  friction for the harness's long-horizon agent-assisted development
  pattern (pragmatic judgment, not a technical elimination).
- Python: rejected — runtime/dependency packaging friction for a
  long-running user service on Arch.
- C++/Qt: rejected — highest complexity and build burden for no decisive
  advantage given QML stays presentation-only (ADR-0001).

## EVIDENCE/SOURCES

- Constraints in docs/PRODUCT.md and docs/ARCHITECTURE.md (single local
  binary, Unix-socket IPC server, HTTP client with incremental sync,
  SQLite, systemd user service).
- No code exists yet; the language was fixed at project bootstrap
  (2026-09-18) before implementation began.
