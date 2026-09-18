# OmaTorrent

A native Omarchy torrent-control experience: Quickshell bar widget, daily-use
panel, and administration dashboard — powered by the qBittorrent WebUI API
through the `omatorrent-service` daemon.

**Status: Phase 0 complete** — the architecture is proven end-to-end
(bar widget → IPC v1 Unix socket → `omatorrent-service` daemon →
qBittorrent WebAPI, real state only; see `docs/agent/PHASE0.md` and
`docs/ROADMAP.md` for the evidence and what comes next).

- Project rules for agents: [AGENTS.md](AGENTS.md)
- Documentation index: `docs/` (start with `docs/PRODUCT.md` and
  `docs/ARCHITECTURE.md`)
- ZCode development harness: [tools/zcode-marketplace](tools/zcode-marketplace)
  and `docs/HARNESS.md`

License: MIT (intended; confirm before first release).
