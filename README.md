<p align="left"><img src="assets/brand/sprout-lockup-dark.png" alt="Sprout" width="376"></p>

# Sprout

Torrent client for Omarchy.

Sprout is a native Omarchy interface and control layer for
**qBittorrent**: a Quickshell bar widget with live transfer state, a
daily-use torrent panel, and an administration dashboard — powered by
the qBittorrent WebUI API through the `omatorrent-service` daemon.
Real state only: every number on screen comes from the daemon, never
fabricated.

It is **not**:

- its own BitTorrent protocol implementation
- a VPN manager
- a NAS admin tool
- a general network monitor

## Status

Phase 0.5 complete — remote qBittorrent connections, credential
storage in the Secret Service, TLS pinning, and the full
bar widget → IPC v1 Unix socket → `omatorrent-service` daemon →
qBittorrent WebAPI chain are proven end-to-end (see `docs/ROADMAP.md`).

Visual identity: [docs/BRAND.md](docs/BRAND.md) (the public name is
Sprout; internal technical identifiers keep the `omatorrent` working
name deliberately until a pre-1.0 migration decision).

- Project rules for agents: [AGENTS.md](AGENTS.md)
- Documentation index: `docs/` (start with [docs/PRODUCT.md](docs/PRODUCT.md) and
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md))
- ZCode development harness: [tools/zcode-marketplace](tools/zcode-marketplace)
  and [docs/HARNESS.md](docs/HARNESS.md)

License: MIT (intended; confirm before first release).
