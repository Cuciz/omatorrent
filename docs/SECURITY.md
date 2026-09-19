# Sprout — Security Model

Status: PHASE 0.5 — remote-backend security model defined BEFORE
implementation (ADR-0008/0009); the standing rules below are enforced
by code and tests where noted; independent adversarial review verdicts
are recorded in docs/agent/ per change.

## Scope [DECISION 2026-09-19]

Sprout is strictly torrent-focused. VPN monitoring is out of scope
entirely (owned by another Omarchy plugin); NAS administration and
general network monitoring are out of scope. Nothing in this file may
be read as permission to grow into those areas.

## Secrets [DECISION — ADR-0009]

- Credentials live ONLY in the Secret Service (via the `secret-tool`
  subprocess, secret crossing stdin/stdout pipes, never argv, never
  temp files). Never in QML (beyond the transient field content of an
  explicit user entry), never in logs, never in SQLite, never in IPC
  responses, never in process arguments, never in Git-tracked or
  daemon-written config (`connection.json` holds a URL, username,
  TLS trust data and policy flags — all non-secret).
- The qBittorrent client fetches the password per login attempt
  (fetch-function, not a retained string); buffers are zeroed after
  use. Residual: GC-managed string copies inside one login call —
  inside the ADR-0004 same-UID boundary.
- The legacy `password` field in `service.json` fails load with an
  explicit migration error (no silent acceptance of plaintext).
- Fail-closed degradation: missing `secret-tool`, locked collection,
  or absent item while the profile requires credentials → status
  `secrets_unavailable`, no login attempts, no prompt loops, no
  plaintext fallback at any layer.
- Honest residual: on machines keeping Omarchy's passwordless default
  keyring, the keyring store is plaintext at rest (0600) — the Omarchy
  baseline, identical to this machine's Chromium/gh credentials; a
  password-protected keyring gets real at-rest encryption.

## Phase 0.5 threat model [DECISION — invariants BEFORE code]

Analyzed threats and their standing mitigations (each pinned by tests
where mechanizable; `docs/TESTING.md` carries the executable matrix):

| Threat | Invariant |
|---|---|
| Credential leakage through logs | No IPC frame or qBittorrent request/response body is ever logged; errors carry classified codes and fixed detail strings only (existing daemon rule, extended to connection messages) |
| Credentials exposed in IPC responses | `connection.*` responses never contain the password, the secret value, or reflections of it; error frames never echo payloads (anti-reflection, v1.0 rule) |
| Credentials stored in SQLite / QML persistence | There is no SQLite yet; QML never persists the password (never `shell.json`, never plugin files); the field is cleared after send |
| Credentials in process arguments | Secret Service via stdin/stdout pipes; qBittorrent password travels in a POST body; argv never carries secrets (Omarchy house rule) |
| Credentials in shell history / Git | No secret ever transits a shell command; secret scan before commits (existing rule); fixtures use fake providers only |
| Secrets in crash reports | Panics log stack traces only; no frame buffers are included |
| Unknowing plain-HTTP remote use | Non-loopback HTTP requires the explicit persisted `allow_insecure_http` acknowledgement; the policy lives in `Profile.Validate`, which EVERY activation path runs — configure, test, persisted-profile LOAD (daemon restart) and the service.json fallback all refuse identically (a forged/stale `connection.json` cannot bypass it); `connection.status`'s `insecure` reports the FACTUAL transport (non-loopback HTTP), never consent — remote HTTP in use with `insecure=false` is unreachable |
| TLS downgrade / silent acceptance | Verification is always on; there is no disable option at any layer; redirects are never followed (host change, scheme change, HTTPS→HTTP and credential-bearing redirects are structurally impossible) |
| Self-signed certificate trust | Only deliberate trust: certificate pinning (fingerprint + anchor PEM, ADR-0008 §4) or an explicit CA bundle; a pin mismatch is a hard failure; the fingerprint crosses IPC, the certificate bytes never do |
| Hostname mismatch | Standard hostname verification stays on in every TLS mode |
| Redirect to a different origin with credentials | No redirect is ever followed (`http.ErrUseLastResponse` on every request, all endpoints, pinned by fixtures) |
| Malicious/compromised qBittorrent endpoint | The adapter caps bodies (64 MiB reads, 64-rune version strings), rejects unexpected statuses, never executes anything from responses; the daemon never trusts backend echo for identity (add cross-check, existing) |
| Oversized/error response bodies | All response reads are limited (`io.LimitReader`); version strings capped daemon-side AND encoder-side |
| Authentication loops / ban hammering | One bounded re-login per request; authentication-class failures back off 30 s→10 min; after 3 consecutive bad-credential logins the syncer goes sticky `auth_failed` (no further backend contact until reconfiguration) — below qBittorrent's 5-attempt/1 h IP ban |
| Session-cookie theft / leakage | Cookies live only in the daemon's in-memory jar, one per backend epoch, discarded on switch (best-effort logout first), never persisted, never crossing IPC |
| Credentials outliving their need | Fetch-per-login provider function; zeroed buffers; test clients are discarded with their jars |
| Local socket clients retrieving secrets | v1.4 responses carry only `has_secret` (bool); no IPC message returns a secret; hostile same-UID clients are inside the documented ADR-0004 boundary (0600 socket, private runtime dir) |
| Hostile config file permissions | `service.json`/`connection.json` must be 0600, UID-owned, no symlink (reads `O_NOFOLLOW`; writes via exclusive temp + fsync + atomic rename in a 0700 directory); permissive/malformed ⇒ fail closed, never repaired |
| Symlinked config/secret paths | Read: refused. Write: rename over the target replaces a symlink non-followingly; the temp file is `O_EXCL`; the socket-path component checks remain (ADR-0004) |
| TOCTOU around config/secret files | All checks and reads happen on open handles (`fstat` not `stat`); writes are exclusive-create + rename; same-UID races remain inside the ADR-0004 boundary |
| Half-applied reconfiguration | Configure is transactional: per-transaction snapshots of the persisted profile (raw bytes) and — for replace/delete — the previous secret; failures restore the CURRENT active state exactly (multi-switch sequences never resurrect stale profiles); `keep` never touches the secret provider; unrecoverable secret snapshots reject before any mutation; base paths reject all percent-encodings so no proxy decoding can escape the configured prefix |
| Malformed URLs | Strict daemon-side validation (scheme allowlist, no userinfo, no query/fragment, host/port/path rules, length caps — ADR-0008 §2); reject `file:`, `ftp:`, `ssh:`, `unix:`, `javascript:`, `data:`, custom schemes, embedded credentials |
| Localhost-vs-remote misclassification | Loopback = literal `127.0.0.1`, `::1`, `localhost` only; policy derives from the classified literal, documented residual: `/etc/hosts` trust (same-UID boundary) |
| SSRF-like configuration to odd schemes/targets | Scheme allowlist + no-userinfo + bounded lengths; the daemon dials exactly the configured origin, no URL from responses is ever followed |
| Mutation retargeting across backends | Configure is refused while mutations are in-flight (`mutations_pending`); pendings are epoch-stamped and can never execute or confirm against a different backend (ADR-0008 §8) |

## Transport [DECISION — ADR-0008]

- TLS with verification actually enabled is the default for remote
  backends; disabled verification is a defect, not a config option
  (there is no such option). Loopback HTTP remains allowed without
  warning (local experience unchanged).
- Auth failure (bad credentials), ban, unreachable, TLS trust and TLS
  hostname failures are distinct error classes with distinct handling
  and distinct daemon status codes (fixture-tested; live
  failed-credential paths are deliberately NOT exercised against the
  real local backend to avoid its 5-attempt IP ban).
- The daemon is never a TCP listener; the only listener is the Unix
  socket below.

## IPC [DECISION — implemented per ADR-0004; v1.4 per ADR-0008]

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
  degraded shapes are returned. The two deliberate exceptions (both
  bounded, both lockstep-disciplined, both documented in ADR-0008):
  v1.2 mutation stage-1 submission (≤ 5 s) and v1.4
  `connection.test` (≤ 8 s, temporary client, no state change).
- v1.1 subscriptions (ADR-0005): read-only; every frame ≤ 4096 bytes
  (snapshot chunked, deltas split, torrent names capped at 512 runes);
  live-delta outbound queue bounded at 256 frames per connection (the
  initial snapshot uses bounded backpressure instead) — a slow or
  malicious subscriber is disconnected, never able to grow daemon
  memory. Version probes and names never include secrets;
  hashes/names/state only.
- v1.2 mutations (ADR-0006, Phase 0.3): the protocol's only
  torrent-mutating surface. Abuse bounds: strict schemas (hashes
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
- v1.4 connection management (ADR-0008, Phase 0.5):
  `connection.status` is cache-served like every status surface.
  `connection.test` carries an optional password ONCE per user action
  (client→daemon, 0600 socket in a private runtime dir — the ADR-0004
  same-UID boundary); it is never echoed, never logged, never
  persisted, capped at 256 bytes, and the parse layer treats its
  presence rules as schema (no co-presence with
  `use_stored_password`). `connection.configure` expresses secret
  intent explicitly (`keep`/`replace`/`delete`) — opening or closing a
  settings form can never erase or expose a secret. Configuration
  activation is the only IPC message that switches the backend epoch
  and refuses while mutations are in flight.
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
- Phase 0.5 adds: a backend switch can never retarget a mutation
  (epoch isolation, ADR-0008 §8).

## Systemd and packaging

- User service only (`packaging/systemd/omatorrent-service.service`,
  Restart=on-failure; hardening candidates documented in the unit).
  Phase 0 runs it unprivileged under `user@.service`.
- Phase 0.5 runtime dependency: `libsecret` (`secret-tool`) — the
  daemon degrades truthfully (`secrets_unavailable`) when absent
  (ADR-0009; recorded in docs/PACKAGING.md).
- Package/install/update scripts are release-audit targets: what runs,
  with which privileges, download verification, install paths.

## Review independence [DECISION]

- Security review is performed by omatorrent-security-reviewer, which is
  never the implementing agent, and is adversarial: it looks for failure
  paths. High-risk changes and every release require its verdict.
  (Phase 0/0.5 verdicts recorded in docs/agent/.)
