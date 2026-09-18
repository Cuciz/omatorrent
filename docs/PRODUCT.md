# OmaTorrent — Product Definition

Status: DRAFT — the durable statement of what OmaTorrent is and is not.
Labels: [DECISION] = settled product direction; [PROPOSAL] = default
intention, changeable without an ADR; [OPEN] = open question.

## What it is

- A native Omarchy torrent-control experience [DECISION]:
  - **Bar**: compact transfer state in the Omarchy bar.
  - **Panel**: daily-use torrent operations on bar click.
  - **Dashboard**: administration, statistics, backend health, storage and
    network safety.
- A control plane: its engine is qBittorrent, driven through the qBittorrent
  WebUI API [DECISION].
- A separate daemon (`omatorrent-service`) owning all business logic, with
  Quickshell/QML as presentation only [DECISION, ADR-0001].

## What it is not

- Not a BitTorrent implementation [DECISION].
- Not a VPN manager; VPN/network monitoring is defense in depth alongside
  correct qBittorrent interface binding [DECISION].
- Not a NAS administrator [DECISION].
- Not a qBittorrent process manager (out of initial core scope) [DECISION].
- Not multi-backend before 1.0: Transmission or others only after an
  explicit later decision [DECISION].

## Product behavior commitments

- Degrades truthfully: every state shown (transfers, backend, VPN, storage)
  has an identifiable real source; disconnected states are explicit
  [DECISION].
- Destructive actions (deletion with file removal) require explicit
  confirmation and protection [DECISION].
- No cloud telemetry by default [DECISION].

## Open questions

- [OPEN] Exact scope split between Panel and Dashboard at 0.1–0.4.
- [OPEN] Remote qBittorrent configuration UX (0.5+).
- [OPEN] Product plugin name/namespace on plugins.omarchy.org (observed
  convention: `author.plugin-name`, e.g. `b.okomart`, `local.networks`).
