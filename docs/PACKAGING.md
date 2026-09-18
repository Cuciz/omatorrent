# OmaTorrent — Packaging & Distribution

Status: PLACEHOLDER — packaging begins at 0.9. Nothing here is implemented;
this file records the decided direction and the open questions.

## Decided direction [DECISION]

- The product ships as an Omarchy Quattro plugin (QML, installed into the
  user plugin area or via plugins.omarchy.org) plus the
  `omatorrent-service` daemon (binary + systemd user unit).
- The development harness plugin (`omatorrent-dev-harness`) is NEVER
  packaged or published as the product — separate identities.

## Release audit targets (from docs/SECURITY.md)

- What install/update scripts execute, with which privileges.
- Download verification for any fetched artifact.
- Install paths; upgrade path from the previous release; rollback path (or
  its documented absence).
- No secrets, placeholder credentials, or machine-specific absolute paths
  in anything shipped.

## Open questions

- [OPEN] Distribution channel for the daemon binary: AUR? GitHub release?
  Both? (Arch rules: prefer official repos/AUR appropriately; decide at 0.9
  with the omarchy-integration standards in mind.)
- [OPEN] Product plugin namespace on plugins.omarchy.org (observed
  convention `author.plugin-name`).
- [OPEN] Version scheme alignment between plugin, daemon, and tags.
