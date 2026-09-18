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
- Same-UID processes are inside the filesystem trust boundary
  (documented in ADR-0004): a same-user attacker can race path checks.
  Cross-UID protection is what the 0600/0700 permissions provide.

## Destructive operations [DECISION]

- Deletion with file removal requires an explicit confirmation flag in
  the IPC contract and UI confirmation; path validation prevents escape
  from the content directory; stale-state mis-targeting must be
  prevented. (No destructive operations exist before 0.3; IPC v1 has no
  mutations.)

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
