# AGENTS.md — OmaTorrent

OmaTorrent is a native Omarchy torrent-control experience: a bar widget with
compact transfer state, a daily-use panel, and an administration dashboard.
Its engine is **qBittorrent** (via the WebUI API); Omarchy Quattro/Quickshell
is the presentation layer.

OmaTorrent is **not**: a BitTorrent implementation, a VPN manager, a NAS
administrator, or a qBittorrent process manager (out of initial core scope).
Transmission support is out of scope before 1.0.

## Architecture invariants (violations require an ADR first)

- QML/Quickshell is **presentation only**; all business logic lives in the
  `omatorrent-service` daemon (Go, ADR-0002) behind a versioned Unix-domain
  IPC protocol (ADR-0003). See ADR-0001.
- qBittorrent is **never** queried from QML; only the daemon's adapter talks
  to the WebUI API.
- Secrets never enter QML, never enter logs, never enter SQLite.
- Destructive file deletion requires explicit protection and confirmation.
- The UI must remain truthful and usable when qBittorrent, NAS, VPN, daemon,
  or Quickshell temporarily fail — degraded states are product behavior.
- VPN/network monitoring is defense in depth on top of correct qBittorrent
  interface binding, never a substitute for it.

## Source of truth

| Knowledge | Location |
|---|---|
| Project map, rules (this file) | `AGENTS.md` |
| Architecture | `docs/ARCHITECTURE.md`, ADRs in `docs/adr/` |
| Product definition | `docs/PRODUCT.md` |
| Roadmap and milestones | `docs/ROADMAP.md` |
| qBittorrent API facts (versioned) | `docs/QBITTORRENT.md` |
| Quickshell/Omarchy rules | `docs/QUICKSHELL.md` |
| IPC contract | `docs/IPC.md` |
| Security model | `docs/SECURITY.md` |
| Testing and done-when | `docs/TESTING.md` |
| Dev environment and commands | `docs/DEVELOPMENT.md` |
| Packaging and release | `docs/PACKAGING.md` |
| This ZCode harness | `docs/HARNESS.md` |
| Durable session checkpoint | `docs/agent/HANDOFF.md` |
| Implementation history | Git (commits are the record) |

Do not duplicate these sources; update them instead.

## Workflow

Small changes: INSPECT → IMPLEMENT → TEST → REVIEW DIFF → REPORT.
Architectural or risky changes: RESEARCH → PLAN (`/ot-plan`) → ARCHITECT
REVIEW → IMPLEMENT (`/ot-implement`) → TEST → SECURITY/QA REVIEW (`/ot-review`)
→ FIX → VERIFY (`/ot-verify`) → ACCEPT. The primary agent owns acceptance.

Parallel agents are for independent research/modules only. Never let two
writable agents modify the same tightly coupled subsystem or shared contract
concurrently.

## Delegation (omatorrent-dev-harness plugin)

| Agent | Use for | Writes |
|---|---|---|
| omatorrent-architect | boundaries, drift, ADR review | no |
| omatorrent-quickshell | QML/Quickshell implementation | yes |
| omatorrent-backend | Go daemon implementation | yes |
| omatorrent-qbt-researcher | qBittorrent API facts | no |
| omatorrent-security-reviewer | adversarial security review | no |
| omatorrent-qa-release | tests, CI, release gates | tests only |

The built-in Explore agent may not receive this file — when delegating to
Explore, always include the relevant invariants in its task description.

## Mandatory behavior

- Search before implementing; do not assume code does not already exist.
- Do not modify unrelated files; no opportunistic refactors.
- Never report tests as passed unless executed; never hide failing tests.
- Use explicit states: PASS / FAIL / NOT RUN / NOT AVAILABLE / BLOCKED.
- Never bypass architecture for speed; surface conflicts, stop, and report.
- Do not place qBittorrent networking in QML. Do not expose secrets to QML.
- Do not introduce cloud telemetry by default.
- Do not delete user data without explicit confirmation.
- Do not silently change an ADR-level decision; record a new ADR first.
- Prefer current project tooling over introducing new dependencies.
- Do not commit or push unless the user asked for it; recommend boundaries.

## High-risk operations (extra care, explicit user awareness)

Deletion with file removal; credential handling and storage; the IPC socket;
remote qBittorrent/SSH/NAS connections; external command execution; updater
and packaging scripts; systemd unit changes; VPN-related claims. For these,
the omatorrent-security-reviewer must be independent from the implementer.

## Validation

A change is complete only with executed evidence (`/ot-verify`, skill
`omatorrent-verification`): build/tests actually run; services verified by
real process/log state; shell UI verified in the running shell. See
docs/DEVELOPMENT.md for the current command set. Runtime validation beats
file inspection.
