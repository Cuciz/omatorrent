---
name: omatorrent-security-review
description: Use for any OmaTorrent work involving secrets or credential storage, networking, TLS, remote qBittorrent connections, the IPC socket and its permissions, destructive file deletion, updater logic, package or install scripts, VPN status handling, NAS or SSH integrations, or systemd unit permissions — and before every release. Runs the independent adversarial security review checklist.
---

# OmaTorrent Security Review Checklist

Applies whenever a change touches a high-risk area. The review is performed
by the omatorrent-security-reviewer agent — independent from whoever
implemented the change — and is adversarial by design: it looks for failure
paths, not justifications.

## High-risk triggers

secrets, credential storage, networking, TLS, remote qBittorrent, IPC socket
permissions, deletion with file removal, updater logic, package/install
scripts, VPN status claims, NAS/SSH integrations, external command execution,
systemd unit permissions, and every release.

## Checklist

1. **Secrets** — locate every place credentials could flow: secret provider
   only, or also SQLite / logs / IPC payloads / QML / process arguments /
   environment visible to other users? Grep, do not assume.
2. **Transport** — remote qBittorrent: TLS verification actually enforced?
   Auth failure vs ban (403) handled distinctly?
3. **IPC socket** — filesystem permissions restrict who may connect; message
   size limits; malformed messages cannot crash either side; version
   handshake rejects downgrade.
4. **Destructive operations** — deletion with file removal requires explicit
   confirmation in the contract; path validation prevents escaping the
   content directory; no deletion of the wrong torrent on stale state.
5. **External commands / SSH / NAS** — argument construction without shell
   interpolation of user-controlled values; host key checking on.
6. **VPN model** — UI must never claim protection the backend can prove;
   monitoring is defense in depth on top of qBittorrent interface binding,
   never a replacement for it.
7. **Updater / packaging scripts** — what executes, with which privileges,
   from which source; download verification.
8. **systemd** — unit hardening appropriate to a user service touching
   secrets and sockets.

## Report

Verdict APPROVE / APPROVE WITH CONDITIONS / REJECT; findings with severity,
file:line evidence, concrete failure scenario, and fix; list of areas checked
and clean; NOT VERIFIED for anything uncheckable.
