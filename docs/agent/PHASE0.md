# Phase 0 — Technical foundation (record)

Tracking: GitHub issue #1; branch `feat/phase0-foundation`.
Status: COMPLETE (pending PR review). Date: 2026-09-18.

## Objective (delivered)

Prove the architecture end-to-end with real state only:

```
Omarchy bar widget (local.omatorrent)
        ↓ IPC v1 (ADR-0004: NDJSON over Unix socket)
omatorrent-service (Go daemon, systemd user service)
        ↓ WebUI API v2 (only qBittorrent-aware component)
qbittorrent-nox 5.2.3 / WebAPI 2.15.1 (live, read-only)
```

No full torrent UI was built (non-goal); no mutations were exercised
against the live backend (non-goal + user's real torrents).

## Reconciliation of prior WIP (2026-09-18)

The pre-existing WIP (docs/IPC.md edit, ADR-0004, earlier PHASE0.md)
scoped the plugin as inert with hello/health only. The Phase 0 goal was
widened to a live end-to-end proof, so the WIP was extended — not
discarded: framing, socket lifecycle and error model kept verbatim;
added request ids, backend-aware health, system.status, and an active
QML client. ADR-0004 records the amendment. The old plan's narrower
non-goals (no qBittorrent adapter, no systemd install) were superseded
by the task; everything else it forbade (SQLite, VPN, NAS, mutations,
packaging claims) remains unbuilt.

## Environment (verified live)

| Component | Version | Source |
|---|---|---|
| Omarchy | 4.0.4-1 (version file says 4.0.0.alpha; package version is authoritative) | `pacman -Q omarchy` |
| Quickshell | 0.3.1-1 | `pacman -Q quickshell` |
| qbittorrent-nox | 5.2.3-3, WebUI 127.0.0.1:8080 (localhost bypass) | `pacman -Q`, `ss -tlnp` |
| qBittorrent WebAPI | **2.15.1** (live probe; docs/QBITTORRENT.md) | `/api/v2/app/webapiVersion` |
| Go | 1.27.1 via mise (repo-scoped; pacman `extra/go` recommended permanently — interactive sudo unavailable to the agent) | `go version` |
| systemd user session | active, XDG_RUNTIME_DIR=/run/user/1000 (0700, uid-owned) | `systemctl --user`, `ls -ld` |

Plugin paths: built-ins `/usr/share/omarchy/shell/plugins/`; user area
`~/.config/omarchy/plugins/` (installed `local.omatorrent` there).
Reference components studied: docs/QUICKSHELL.md.

OmaqBT prior art: NOT PRESENT on this machine — nothing inspected or
claimed. Closest local prior art is `local.networks` (panel doing
qBittorrent XHR directly from QML — the anti-pattern ADR-0001 forbids;
documented in docs/QUICKSHELL.md).

## IPC v1 (ADR-0004, docs/IPC.md)

NDJSON chosen over JSON-RPC (lockstep protocol; LF framing maps to
Quickshell SplitParser). Ops: hello, health (backend ok/unavailable),
system.status (versions, speeds, torrent count; degraded shape).
torrent.snapshot deferred to 0.2 with the incremental-sync design.
Request ids required; error responses never echo payload/ids.

## Evidence matrix (executed 2026-09-18)

### Harness / repo
| Check | Result |
|---|---|
| `python3 tools/validate_harness.py` | PASS (82/82) — before and after changes |
| `bash tools/test_guard_hook.sh` | PASS (39/39) |

### Daemon (Go) — `cd omatorrent-service`
| Check | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | PASS (clean) |
| `go test -race ./...` | PASS (ipc, qbittorrent, state, config) |

### IPC contract (real sockets, in `go test`)
hello/handshake; version_mismatch (protocol 2); handshake_required
(health-first); 10 malformed classes → invalid_message without crash;
exact-4096 accepted / 4097 → message_too_large; unknown type-only →
unsupported_message after handshake; second hello rejected; clean
disconnect; reconnect after orderly restart; stale socket refused;
client limit (17th closed silently); socket 0600 + dir 0700; unsafe
parent dirs refused; shutdown closes stalled clients + removes socket
by identity — **all PASS**.

### qBittorrent adapter (fixtures)
login success (SID+Referer), bad credentials, ban (403), localhost
bypass, SID-expiry re-login (exactly one retry), unauthorized without
creds, decode errors, unreachable, invalid base URL — **all PASS**.

### Live integration (read-only, user's real backend)
version/WebAPI probes; transfer/info; torrents/info (3 torrents);
sync/maindata rid=0 snapshot + rid=1 delta behavior; daemon under
systemd: start → "backend reachable" (v5.2.3/2.15.1) → ot-probe hello/
health/system.status with real data → SIGTERM/restart clean — **all
PASS** (outputs in session log; reproducible via docs/DEVELOPMENT.md).

### Shell / plugin
| Check | Result |
|---|---|
| `omarchy plugin validate plugins/local.omatorrent` | PASS (exit 0) |
| `bash tools/test_quickshell.sh` (isolated `qs` speaking IPC v1) | PASS (hello + ≥2 status responses) |
| Bar renders live state (`qBT ●` idle; `qBT OFFLINE` daemon-down; reconnects on daemon restart) | PASS — observed via screenshots (docs/screenshots/) |
| Journal clean (no QML errors with final code) | PASS |
| Degraded: daemon stopped → widget `qBT OFFLINE`; daemon restarted → auto-reconnect to `qBT ●` | PASS (observed live) |
| Degraded: qBittorrent unreachable → `qBT ERROR` | PASS (contract + fixture level); NOT observed live — stopping the user's real qbittorrent-nox was out of bounds |

### Security / architecture
Independent adversarial reviews were run read-only after implementation
(implementer excluded). Verdicts and findings: recorded below when
received; CRITICAL/HIGH findings would block the PR.

### Lifecycle quirks discovered (documented in docs/DEVELOPMENT.md)
- Quickshell `Socket.write` sends no line terminator — append `\n`.
- Initial `connected: true` doesn't fire `connectionStateChanged` —
  bootstrap-aware hello required.
- Instantiated bar widgets can survive plugin rescans with stale code —
  `omarchy-restart-shell` after edits.
- SIGKILL leaves a stale socket; since review round 2 the next start
  recovers it safely (proven-dead probe + identity re-check) — a live
  daemon on the socket or any ambiguous state still fails closed.

## PR review round 2 (2026-09-18, on PR #2 before merge)

Three findings addressed:

1. **One request in flight (BarWidget.qml)**: pending-id tracking; no new
   system.status while one is outstanding; responses matched by id
   (mismatches ignored); pending state reset on disconnect/reconnect plus
   a 6 s stuck-response guard that drops the session to the reconnect
   path. The smoke test now exercises the same discipline including a
   deliberate in-flight violation probe (response for the foreign id is
   ignored).
2. **Cache-only status**: Snapshot/Health never contact qBittorrent —
   the synchronous first fetch was removed; before the background
   refresher's first cycle completes, IPC answers the degraded
   loading/unavailable shape. Eliminates the startup race and the
   first-request stampede (test counts backend calls through a fake).
   Options.FetchSync removed; comments, IPC.md, SECURITY.md, ADR-0004
   updated to match behavior.
3. **Safe stale-socket recovery (ADR-0004 amendment 2)**: an existing
   socket is removed only when it has the exact expected shape (socket
   type, no symlink, UID-owned, 0600), its listener is proven dead
   (connect → ECONNREFUSED), and the file identity still matches between
   probe and unlink. A full IPC v1 hello that receives the exact
   expected response means a live daemon owns the socket → startup
   refused. Any ambiguity (connect timeout, unexpected answer, wrong
   shape/owner/permissions, symlink, non-socket) fails closed with the
   path untouched. New tests: stale recovered, live daemon refused,
   wrong perms refused untouched, symlink refused untouched,
   hanging-listener (ambiguous) refused untouched, non-socket file
   refused.

## Review verdicts (round 2, after the fixes above)

- **Security re-review: PASS-WITH-FINDINGS** — all six prior fixes
  VERIFIED (stale recovery, cache-only status, userinfo rejection,
  config hardening, one-in-flight widget, unit hardening). Explicit
  confirmations: recovery cannot delete a cross-UID socket (all race
  windows same-UID-only, inside the documented boundary); no IPC request
  path touches qBittorrent; no secret reaches logs/QML. New findings
  were 2 LOW + 4 INFO; applied: byte-capped probe read (LOW), ENOENT
  re-Lstat during probe, config symlink-refusal test. Documented as
  accepted/deferred: SplitParser buffers before the QML length guard
  (Quickshell-side, same-UID boundary, 0.x), loading-vs-error wire
  indistinguishable (deliberate v1 shape), process-global umask,
  identity-swap race test gap.
- **Architecture re-review: APPROVE-WITH-NOTES** — all three PR
  findings VERIFIED (one-in-flight stays presentation-side per
  docs/IPC.md client obligations; ADR-0004 amendment 2 ↔ IPC.md ↔
  SECURITY.md ↔ code consistent; no scope violations, no new dead
  code). Notes applied: stale PHASE0.md bullet corrected,
  replaced-path shutdown test added (TestShutdownLeavesReplacedSocket),
  symlink-untouched assertion strengthened. Noted, accepted: probe frame
  literal vs contracts file (frozen v1 grammar), 6 s stuck-guard has no
  automated coverage (documented gap for 0.x).

## Review verdicts (round 1)

Independent read-only reviews after implementation (implementer excluded;
full texts in the PR conversation record):

- **Security review: PASS-WITH-FINDINGS** (no CRITICAL/HIGH). Confirmed:
  no secret reaches QML/logs/IPC; malformed IPC input cannot crash the
  daemon; socket/config permissions enforced as documented (live-verified
  0600/0700, no TCP listeners). Fixed after review: URL-userinfo
  credentials now rejected at client construction (MEDIUM — would have
  been logged); config stat-via-handle + O_NOFOLLOW before read; QML
  line-length guard. Applied zero-cost hardening: NoNewPrivileges +
  PrivateTmp in the systemd unit. Deferred with rationale (INFO-level):
  singleflight first-fetch, torrents/info-for-count tradeoff, stale-
  socket restart churn (documented in the unit), startup race noted by
  reviewer (resolved by process exit).
- **Architecture review: APPROVE-WITH-NOTES** (no invariant violations).
  Confirmed: QML is presentation-only (poll/backoff are documented client
  obligations, not business logic); package boundaries and dependency
  direction correct; IPC implementation byte-consistent with
  docs/IPC.md/ADR-0004; manifest matches installed Omarchy 4.0.4 schema;
  no Phase 0 scope violations. Fixed after review: README status,
  dead interface surface (Backend.Login, SocketPath(), unused JSON tags
  /LastOK), version-probe cadence (only while unknown or after failure;
  comment corrected), handshake-phase clean-EOF symmetry, redundant
  health-on-hello removed, XDG_RUNTIME_DIR guard in QML. Deliberately
  kept: TransferInfo.Status field (endpoint shape, consumed at 0.2),
  WidgetButton explicit sizing (proven rendering, moon-phase pattern).

## Deferred (by design)

torrent.snapshot + mutations (0.2/0.3 with confirmation flow), SQLite
(0.8), VPN (0.6), NAS (0.7), remote qBittorrent + TLS + secret provider
(0.5), CI (0.9), packaging, public plugin namespace, push-based IPC
updates.
