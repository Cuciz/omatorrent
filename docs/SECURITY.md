# OmaTorrent — Security Model

Status: PHASE 0 IMPLEMENTED — the standing rules below are enforced by
code and tests where noted; independent adversarial review verdicts are
recorded in docs/agent/PHASE0.md per change.

## Secrets [DECISION]

- Credentials (qBittorrent WebUI, remote/NAS/SSH) live ONLY in the secret
  provider. Never in QML, never in logs, never in SQLite, never in IPC
  responses, never in process arguments.
- Phase 0 state: the credential source is the config file
  (`$XDG_CONFIG_HOME/omatorrent/service.json`, optional `username`/
  `password`), refused at load if group/world-accessible (tested:
  `internal/config/config_test.go`). The local dev backend needs no
  credentials (localhost bypass). A real secret provider
  (systemd `LoadCredential=`/libsecret) is scheduled with remote-backend
  work (0.5); the config-file stand-in is a documented, reviewed interim.
- No secrets or placeholder credentials in the repository (secret scan
  before every commit).

## Transport [DECISION]

- Remote qBittorrent connections use TLS with verification actually
  enabled; disabled verification is a defect, not a config option.
  (Phase 0 ships localhost HTTP only; remote+TLS lands in 0.5.)
- Auth failure (bad credentials) and ban (HTTP 403 after repeated
  failures) are distinct error classes with distinct handling
  (implemented + fixture-tested in `internal/qbittorrent/client.go`).
- The daemon is never a TCP listener; the only listener is the Unix
  socket below.

## IPC [DECISION — implemented per ADR-0004]

- Unix-domain socket at `$XDG_RUNTIME_DIR/omatorrent/service.sock`:
  application dir 0700, socket 0600 (umask 077 + explicit chmod,
  contract-tested). Runtime dir validated (absolute, UID-owned, 0700,
  no symlink components); unsafe paths are refused — never repaired.
  An existing socket is removed only when proven stale: exact expected
  shape (socket type, UID-owned, mode 0600, no symlink) plus a
  dead-listener probe (ECONNREFUSED), a file-identity re-check between
  probe and unlink, and a full IPC v1 hello probe — a live answer means
  another daemon owns the socket and startup is refused. Ambiguous
  states (timeouts, garbage, wrong shape) fail closed; nothing is
  deleted blindly. Shutdown removes the socket only after a dev/ino
  identity match.
- Versioned handshake; incompatible versions rejected safely
  (version_mismatch, tested).
- Malformed messages never crash either side; frames bounded at 4096
  bytes incl. LF; duplicate keys, unknown fields, null values,
  non-integer numbers, invalid UTF-8 and trailing JSON all rejected as
  invalid_message (tested). Error responses never echo payload or ids.
- Resource limits: 16 clients (excess closed silently), 5 s handshake
  deadline, 30 s idle read deadline, 5 s write deadline. Status/health
  are served exclusively from the background refresher's cache — no IPC
  request ever contacts qBittorrent; before the first refresh the
  degraded shapes are returned.
- v1.1 subscriptions (ADR-0005): read-only; every frame ≤ 4096 bytes
  (snapshot chunked, deltas split, torrent names capped at 512 runes);
  live-delta outbound queue bounded at 256 frames per connection (the
  initial snapshot uses bounded backpressure instead) — a slow or
  malicious subscriber is disconnected, never able to grow daemon
  memory. Version probes and names never include secrets;
  hashes/names/state only.
- v1.2 mutations (ADR-0006, Phase 0.3): the protocol's only
  backend-mutating surface. Abuse bounds: strict schemas (hashes
  40/64-hex, magnet URLs ≤ 2048 bytes, refs ≤ 128 chars of a fixed
  charset — all enforced at parse time before any backend call); one
  mutation in flight per connection (lockstep handlers) and a
  daemon-wide cap of 4 concurrent submissions (excess → `busy`); the
  completed-ref ring is bounded (64) so replay traffic cannot grow
  memory; mutation submission is bounded at 5 s and reconciliation at
  10 s per mutation. Rejections never echo payloads (anti-reflection);
  no secrets cross the boundary. Destructive intent requires an
  explicit boolean (`delete_files`) — ambiguous or missing values are
  protocol violations, and the adapter always forwards the boolean
  explicitly to qBittorrent (never a backend default, never `all`
  keyword, one hash per call).
- Same-UID processes are inside the filesystem trust boundary
  (documented in ADR-0004): a same-user attacker can race path checks.
  Cross-UID protection is what the 0600/0700 permissions provide.

## Destructive operations [DECISION]

- Implemented in Phase 0.3 per ADR-0006: removal WITHOUT files is a
  normal confirmed action; removal WITH files is contractually and
  visually distinct — an explicit, separately confirmed
  `delete_files: true` boolean at the IPC layer (missing/non-boolean ⇒
  connection-closing protocol violation; no defaulting, no inference),
  an urgent-colored separate confirm path in the panel, an explicit
  `deleteFiles` form value to qBittorrent, and stale-state mis-targeting
  prevented by `stale_torrent` validation against committed state
  before any submission. Replay safety: a completed ref can never
  re-execute (recorded terminal outcome returned); remove is never
  retried blindly after an ambiguous timeout. Residual documented
  limit: ref deduplication is per-daemon-process, not durable across
  daemon restarts (clients must require fresh user action; see
  ADR-0006).

## VPN safety model [DECISION]

- Correct qBittorrent interface binding is the primary control. VPN/
  network monitoring is defense in depth that warns, not a mechanism
  that asserts protection it cannot prove. UI claims must be backed by a
  real source. (0.6.)

## Systemd and packaging

- User service only (`packaging/systemd/omatorrent-service.service`,
  Restart=on-failure; hardening candidates documented in the unit).
  Phase 0 runs it unprivileged under `user@.service`.
- Package/install/update scripts are release-audit targets: what runs,
  with which privileges, download verification, install paths.

## Review independence [DECISION]

- Security review is performed by omatorrent-security-reviewer, which is
  never the implementing agent, and is adversarial: it looks for failure
  paths. High-risk changes and every release require its verdict.
  (Phase 0 verdict recorded in docs/agent/PHASE0.md.)
