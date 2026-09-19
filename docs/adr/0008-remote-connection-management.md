# ADR-0008: Remote connection management, IPC v1.4 and backend epochs

STATUS: ACCEPTED (Phase 0.5)

## CONTEXT

Phase 0.x supports exactly one hard-wired local backend
(`http://127.0.0.1:8080`, optional config-file credentials that the
localhost auth bypass makes unnecessary). Phase 0.5 adds remote
qBittorrent backends (LAN/NAS, HTTPS reverse proxy, remote host over an
existing secure path) with a runtime-configurable connection profile, a
test-before-save settings surface, and hot backend switching — while
keeping v1.0–v1.3 IPC shapes byte-compatible and the local experience
credential-free.

Research constraints that bind this design (docs/QBITTORRENT.md,
Phase 0.5 section; all source-verified against qBittorrent v5_2_x with
live probes where safe):

- The WebUI login contract is **version-dependent**: 5.2 answers 204 /
  401 / 403-with-ban-message; ≤ 5.1 answers `200 "Ok."` / `200
  "Fails."` / 403. The session cookie is named `QBT_SID_<port>` on
  5.2 and `SID` on ≤ 5.1 — the client must never hardcode it.
- Requests carrying **neither `Origin` nor `Referer` are explicitly
  allowed** on 4.6→5.2; sending a Referer is neither required nor
  proxy-robust.
- **5 failed logins ban the client IP for 1 h** (per resolved IP,
  in-memory). A polling daemon with a bad password must not
  ban-hammer: auth failures need a sticky terminal state, not just
  backoff.
- qBittorrent never serves under a URL prefix itself; official proxy
  recipes publish it under one and **strip the prefix at the proxy**.
  A base URL with a path component is therefore a legitimate client
  configuration.
- qBittorrent's API **never legitimately redirects**; redirects are
  refused by the existing adapter and stay refused everywhere.
- Built-in HTTPS is one-port/one-protocol with no redirect; self-signed
  certs are the common small-deployment case and need a deliberate
  trust mechanism, not a verification-off switch.

## DECISION

### 1. Connection profile (single backend)

One active backend, ever, before 1.0. Daemon-owned store at
`$XDG_CONFIG_HOME/omatorrent/connection.json`:

- Strict JSON schema (unknown fields rejected), bounded fields,
  fail-closed on malformed content; write path: 0700 directory, `O_NOFOLLOW`
  everywhere, temp file created `O_CREAT|O_EXCL` 0600 in the same
  directory, fsync, atomic rename; the target path is fixed — the
  daemon never writes anywhere else, and a symlinked target is
  replaced by rename (rename does not follow symlinks) while reads
  refuse symlinks outright.
- Fields: `url` (validated, normalized), `username` (non-secret),
  `tls_mode` (`system` | `ca` | `pin`), `ca_path` (PEM path, `ca`
  mode only), `pin_fingerprint` (64 lowercase hex, `pin` mode only),
  `pin_cert_pem` (PEM of the deliberately trusted certificate, `pin`
  mode only, written only by the daemon), `allow_insecure_http`
  (bool). `mode` (`local`/`remote`), `host` label and `transport`
  are **derived**, never stored.
- `service.json` keeps its Phase 0 role (defaults, IPC socket
  override) minus credentials: a non-empty `password` there now fails
  load with an explicit migration error. Until the user configures
  via IPC, an absent `connection.json` falls back to the `service.json`
  `qbittorrent.url`/`username` in memory (typical local setups never
  need a `connection.json`).

### 2. URL validation (daemon-side, strict)

Accepted: `scheme://host[:port][/path]` with scheme exactly `http` or
`https`. Rejected: any other scheme (`file:`, `ftp:`, `ssh:`, `unix:`,
`javascript:`, `data:`, custom), embedded userinfo (`user:pass@`),
empty host, empty/invalid port, non-empty query or fragment, control
characters, non-ASCII spaces, `..`/`.` path segments, backslashes, a
path longer than 128 bytes, or a total URL over 2048 bytes. The path
is normalized to a leading `/` without trailing slash (reverse-proxy
prefix model: requests go to `{base}/api/v2/…`; the proxy strips the
prefix — pinned by tests). Loopback classification for policy:
literal `127.0.0.1`, `::1`, and the hostname `localhost`
(`/etc/hosts` trust documented as a same-UID-boundary residual).

### 3. HTTP/HTTPS policy

- Loopback (`127.0.0.1`, `::1`, `localhost`): HTTP allowed, no
  warning — the local default keeps working with zero setup.
- Non-loopback HTTP: allowed **only** with the explicit, persisted
  `allow_insecure_http` acknowledgement; activation and test refuse
  with status `insecure_http` otherwise, and the UI must render the
  acknowledged state as INSECURE. HTTPS is the default remote mode.

### 4. TLS policy (fail-closed, no disable switch)

All modes keep hostname verification and standard chain verification
on; there is no `InsecureSkipVerify` anywhere and no
"disable verification" option at any layer.

- `system` — system trust store (default).
- `ca` — `RootCAs` replaced by an explicit PEM bundle (advanced,
  config-file only; not settable over IPC).
- `pin` — deliberate trust of exactly one certificate (TOFU):
  the pinned certificate is added as the pool anchor (self-signed
  case) **and** a `VerifyPeerCertificate` hook asserts the presented
  leaf's SHA-256 equals `pin_fingerprint` (CA-signed case). A pin
  mismatch is a hard failure (`tls_untrusted`), never a downgrade;
  the pin flow is: test fails untrusted → the test response carries
  the offered cert's fingerprint → the user explicitly trusts it →
  configure stores fingerprint+PEM (the daemon captures the offered
  certificate bytes from the failed handshake in a bounded in-memory
  cache; certificate bytes never cross IPC — only the 64-hex
  fingerprint does).

### 5. Redirect policy

No automatic redirects for any authenticated or unauthenticated
request, on any endpoint (existing `http.ErrUseLastResponse` rule,
now pinned by tests): host change, scheme change, HTTPS→HTTP and
credential-bearing redirects are all structurally impossible because
no redirect is ever followed.

### 6. Authentication lifecycle and frugality

- Version-adaptive login: 204 or `200 "Ok."` = success; 401 or `200
  "Fails."` = bad credentials; 403 = banned (login) or expired/absent
  session (API). Cookie name is never assumed (jar-based).
- Neither `Origin` nor `Referer` is sent (explicitly permitted
  4.6→5.2).
- Credentials come from the secret provider as a **fetch-per-login
  function** (ADR-0009); the client never retains the password.
- 403 mid-session triggers **one** bounded re-login per request
  (existing rule); re-login resets rid semantics naturally (new
  session ⇒ qBittorrent answers `full_update:true`).
- **Auth-failure frugality**: authentication-class failures use a
  dedicated backoff ladder (30 s doubling, 10 min cap) and become
  **sticky** after 3 consecutive bad-credential login attempts: the
  syncer stops contacting the backend entirely and reports
  `auth_failed` until the connection is reconfigured — a wrong
  stored password can never reach the 5-attempt IP ban through
  OmaTorrent's polling. `banned` (403 ban message) is its own status
  with the same ladder.
- Logout: on backend switch the daemon makes one best-effort
  `auth/logout` POST with the old session (no credentials in the
  request, 2 s timeout, result ignored) before discarding the old
  client and cookie jar. (Implemented after the architecture review
  flagged the gap; pinned by `TestSwitchLogsOutOldSession`.)

### 7. Error model (daemon status codes)

`connecting` · `connected` · `unreachable` · `auth_required` (backend
demands auth, none configured/available) · `auth_failed` (credentials
rejected) · `banned` · `tls_untrusted` (incl. pin mismatch) ·
`tls_hostname` · `secrets_unavailable` (provider missing/locked/error
— ADR-0009) · `insecure_http` (policy refusal, test/configure only) ·
`invalid_configuration` · `backend_error` (protocol-level surprises).
Only reliably identifiable classes exist; a fixed `detail` string may
accompany, never reflecting request payloads.

### 8. Backend epochs and switch safety

- A monotonic per-process **epoch** (uint64, starts at 0 on daemon
  start and increments on every configured switch; resets on daemon
  restart — documented) identifies the active backend
  identity. Every sync cycle captures the epoch at start; a commit
  whose epoch is stale is discarded (a cycle racing the switch can
  never publish old-backend state).
- Switching (`connection.configure` accepted): refuse while any
  mutation is in-flight (`mutations_pending`); then atomically —
  under the respective locks — swap the adapter in syncer and
  mutator, terminate the old sync session (best-effort logout, rid
  reset, cookie jar discarded with the old client, version re-probe
  flag reset), **clear all torrent state and publish it as removals**
  (subscribers can never see backend A torrents as backend B's), and
  kick a fresh full sync. Pending mutations are additionally stamped
  with the epoch as defense in depth; a cross-epoch pending settles
  as `timeout` (ambiguous), never as a mutation executed against the
  wrong backend.
- Hot reconfiguration (no daemon restart) is chosen deliberately:
  restarts lose IPC continuity for every surface, while an epoch
  switch is bounded, lock-disciplined and testable. A controlled
  restart remains a valid fallback documented in DEVELOPMENT.md.

### 9. IPC v1.4 (additive; v1.0–v1.3 shapes unchanged)

Three request types after hello, strict per-type key sets, frames
≤ 4096 bytes, error frames never echo payloads; `unsupported_message`
list extended.

- `connection.status` `{type,id}` → cache-served profile metadata +
  live status: `configured`, `mode`, `host`, `transport`, `insecure`,
  `username` (non-secret; the settings form needs it — status
  surfaces display only the host label), `has_secret`, `tls_mode`,
  `status`, `detail`, `epoch`.
- `connection.test` `{type,id,url,username?,password?|
  use_stored_password?,tls_mode,pin?,allow_insecure_http?}` →
  one-shot probe with a **temporary client** (separate cookie jar,
  discarded; no state change; no torrent mutations; no login when
  neither `password` nor `use_stored_password` is present). Response:
  `result` (`ok`/`failed`), `status` code, `host`, `transport`,
  `app_version`/`webapi_version` (iff ok), `offered_fingerprint`
  (iff a certificate was presented and rejected — enables the pin
  flow). Bounded inline network work under the lockstep discipline
  (≤ 8 s total; v1.2 mutation stage-1 precedent). `password` is
  never echoed, never logged, never persisted.
- `connection.configure`
  `{type,id,url,username?,secret_action:"keep"|"replace"|"delete",
  password?(iff replace),tls_mode,pin?,allow_insecure_http?}` →
  validate → secret op via the provider (explicit intents; an empty
  form can never erase or expose a secret) → atomic config write →
  epoch switch → fresh full sync. Responses: `connection.configured`
  (`epoch`, `host`, `transport`, `mode`) or `connection.rejected`
  with a fixed code (`invalid_url`, `insecure_http`,
  `secrets_unavailable`, `mutations_pending`, `pin_unknown`,
  `storage_error`).

  Post-review amendments (implemented): credential-bearing
  `connection.test` logins are paced (one per 5 s window; excess
  refused without backend contact) so a retry loop cannot walk into
  qBittorrent's IP ban (security review finding 4).

  Transactional activation (external review round 2, blockers 2+3):
  every Configure transaction snapshots its rollback targets LOCALLY
  at start — the persisted profile as RAW BYTES (exact restore, never
  a re-marshal; an unpersisted fallback restores to "no file"; an
  unreadable-but-present store refuses the transaction up front) and,
  for `replace`/`delete`, the previous secret's exact presence/value.
  `keep` never touches the provider and never participates in secret
  rollback. A `replace`/`delete` whose previous-state snapshot cannot
  be read is rejected (`secrets_unavailable`) BEFORE any mutation — a
  provider error is never interpreted as "no secret exists". On any
  post-snapshot failure the transaction restores exactly what was
  snapshotted: the currently active state, never a stale earlier one
  (A→B succeeding then B→C failing restores B — regression-pinned
  including the restart path). If a restoration itself fails, the
  original rejection code is returned and the failure is logged with a
  classified, secret-free message; the mismatch then surfaces
  truthfully through the connection status (documented decision: no
  separate IPC rollback-failure code).

Compatibility: no existing message shape gains a field; clients that
never send `connection.*` see no difference. The protocol stays
major-version 1 (extension v1.4).

### 10. Settings surface (native Omarchy)

Omarchy has no plugin-settings manifest; the native pattern is an
inline edit mode inside the panel. Decision: **one canonical settings
view as a mode of the existing torrent panel** (gear glyph in the
panel header), reachable from the dashboard footer via the documented
host CLI (`omarchy-shell shell summon local.omatorrent` — summons
cannot carry payloads, so the dashboard opens the panel; settings is
one click away, and the panel offers it directly whenever the
connection is degraded). No third plugin, no duplicate implementation,
no settings state in `shell.json` (that would persist secrets in
plaintext — forbidden). The password field is `Ui.TextField {
password: true }`; its content lives in QML only until the
`connection.test`/`connection.configure` frame is written, then the
field is cleared. No stored secret ever crosses back into QML — the
form renders "stored" from `has_secret`, not from the secret.

## CONSEQUENCES

- The daemon becomes the sole writer of `connection.json`; hand
  editing remains possible (validated, fail-closed) but the IPC
  surface is the primary path.
- `connection.test`/`connection.configure` are the only IPC messages
  that trigger inline network I/O besides v1.2 stage-1 submissions;
  both are bounded and lockstep-disciplined.
- Remote logins put a secret-bearing frame on the local Unix socket
  exactly once per user action (configure/test), inside the
  ADR-0004 same-UID trust boundary; documented rather than
  re-architected (a per-secret-entry helper was considered and
  rejected as overengineering for a 0600 socket in a private runtime
  directory).
- v5.2's 202-Accepted add responses and the 204/401 login contract
  become adapter behaviors pinned by fixtures (the live 5.2.3 backend
  cannot exercise failed-credential paths without ban risk).

## ALTERNATIVES

- **Daemon restart on reconfiguration** — simpler, but destroys IPC
  continuity for every surface and makes the settings UX feel broken;
  rejected for 0.5 (documented fallback).
- **qBittorrent API-key auth (≥ 5.2.0)** — stateless, no ban risk;
  rejected for 0.5 (version-gated, would strand 4.6–5.1 users);
  recorded as a future profile option.
- **Fingerprint-only pinning without the PEM anchor** — impossible
  with stdlib verification semantics without `InsecureSkipVerify`
  (forbidden); the fingerprint+anchor-PEM pair keeps standard
  verification intact in both self-signed and CA-signed cases.
- **A dedicated settings overlay plugin** — rejected: Omarchy's
  native pattern is panel-inline; a third plugin adds surface area
  without adding capability.
- **Rejecting base-path URLs** — rejected: official proxy recipes
  publish qBittorrent under a prefix; supporting it is one
  concatenation rule pinned by tests.

## EVIDENCE / SOURCES

- qBittorrent source `v5_2_x` / `v5_1_x` / `v5_0_x` / `v4_6_x`
  (`src/webui/webapplication.cpp`, `src/webui/api/authcontroller.cpp`,
  `src/base/preferences.cpp`, `src/base/http/server.cpp`,
  `src/webui/api/synccontroller.cpp`) + official wiki (WebUI API,
  NGINX/IIS/Traefik reverse-proxy pages) + live probes on the
  installed 5.2.3 — full matrix in docs/QBITTORRENT.md (Phase 0.5).
- On-machine Omarchy settings-convention research (PluginRegistry
  schema, first-party panel forms, `Ui.TextField` password property,
  network-panel stdin-not-argv precedent), 2026-09-19.
- ADR-0004 (socket trust boundary), ADR-0009 (secret provider),
  ADR-0005/0006 (subscription and mutation contracts preserved).
