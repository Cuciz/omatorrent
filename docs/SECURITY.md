# OmaTorrent — Security Model

Status: DRAFT — the standing rules reviewers enforce (see the
omatorrent-security-review skill). Product implementation has not started.

## Secrets [DECISION]

- Credentials (qBittorrent WebUI, remote/NAS/SSH) live ONLY in the secret
  provider. Never in QML, never in logs, never in SQLite, never in IPC
  responses, never in process arguments.
- No secrets or placeholder credentials in the repository.

## Transport [DECISION]

- Remote qBittorrent connections use TLS with verification actually enabled;
  disabled verification is a defect, not a config option.
- Auth failure (bad credentials) and ban (HTTP 403 after repeated failures)
  are distinct error classes with distinct handling.

## IPC [DECISION]

- Unix-domain socket with explicit filesystem permissions (who may connect).
- Versioned handshake; incompatible protocol versions rejected safely.
- Malformed messages must never crash either side; bounded message sizes.

## Destructive operations [DECISION]

- Deletion with file removal requires an explicit confirmation flag in the
  IPC contract and UI confirmation; path validation prevents escape from the
  content directory; stale-state mis-targeting must be prevented.

## VPN safety model [DECISION]

- Correct qBittorrent interface binding is the primary control. VPN/network
  monitoring is defense in depth that warns, not a mechanism that asserts
  protection it cannot prove. UI claims must be backed by a real source.

## Systemd and packaging

- User service (not system) expected; unit hardening appropriate to a
  component handling secrets and a socket.
- Package/install/update scripts are release-audit targets: what runs, with
  which privileges, download verification, install paths.

## Review independence [DECISION]

Security review is performed by omatorrent-security-reviewer, which is
never the implementing agent, and is adversarial: it looks for failure
paths. High-risk changes and every release require its verdict.
