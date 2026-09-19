# ADR-0009: Credential storage via the freedesktop Secret Service

STATUS: ACCEPTED (Phase 0.5)

## CONTEXT

Phase 0 used the config file (`service.json`, 0600) as an interim
credential source and explicitly scheduled a real secret provider with
the remote-backend work (docs/SECURITY.md, ADR-0002 era). Phase 0.5
introduces remote qBittorrent backends where credentials become the
norm, not the exception, so the interim becomes unacceptable: passwords
must never sit in QML, SQLite, IPC responses, logs, process arguments,
Git-tracked config, or environment persisted in unit files.

Requirements for the provider:

1. Available and functional on a standard Omarchy installation, inside
   a systemd user service (no interactive unlock prompts possible).
2. Survives daemon restarts; updatable at runtime from the connection
   settings surface (keep / replace / delete).
3. No plaintext fallback path in the daemon, ever.
4. No new large dependency (the daemon is stdlib-only today).

Research performed on this workstation (2026-09-19, evidence below):

- gnome-keyring is a **hard dependency of the `omarchy` package**
  (`pacman -Qi gnome-keyring` → `Required By: omarchy`);
  `org.freedesktop.secrets` is active under `user@.service`, and
  libsecret/`secret-tool` is part of the base package set.
- Omarchy deliberately provisions a **passwordless default keyring**
  (SDDM's `pam_gnome_keyring` auth line is removed by
  `/usr/share/omarchy/install/login/sddm.sh`; the plaintext skeleton
  `Default_keyring` is created by
  `/usr/share/omarchy/install/user/default-keyring.sh`). The keyring
  therefore auto-unlocks without any PAM interaction or prompt — proven
  by a live store/lookup/clear round-trip executed from a
  non-graphical `manager`-class session, the same context a systemd
  user service runs in.
- `secret-tool` has **no argv parameter for the secret at all**: store
  reads the secret from stdin; lookup writes it to stdout; only label
  and attribute pairs appear on the command line. This matches
  Omarchy's own house rule (network panel Model.js: "argv is
  world-readable in /proc, so the secret must never be an argument").
- Alternatives: `systemd-creds --user` works without root but rotates
  only via credential-file rewrite + service restart; `godbus` /
  `zalando/go-keyring` would add dependencies for exactly one secret;
  KWallet is installed but not the active Secret Service provider.

## DECISION

The daemon's credential provider is the freedesktop Secret Service,
accessed by spawning `secret-tool` with the secret crossing **only via
stdin/stdout pipes** (never argv, never temp files):

- Item attributes: `service=omatorrent`, `kind=qbt-webui-password`;
  label "OmaTorrent qBittorrent WebUI password" (pure ASCII: GLib
  converts labels from the locale charset, and the daemon's minimal
  child environment runs under C locale where non-ASCII aborts the
  store — live-verified finding, commit 1cfd2cb). One secret exists
  (single-backend product; a second kind would require a new ADR).
- `internal/secrets.Provider` interface (`Store`, `Get`, `Delete` —
  unavailability is the `ErrUnavailable` error class, surfaced as the
  distinct `secrets_unavailable` connection status); the `secret-tool`
  implementation is the only production
  backend; unit tests use a fake provider. The interface keeps a
  provider swap (KWallet, go-keyring) non-architectural.
- Each operation spawns the tool with a bounded timeout (5 s), a
  minimal environment (PATH, HOME, DBUS_SESSION_BUS_ADDRESS /
  XDG_RUNTIME_DIR), and classified error reporting only — subprocess
  stderr is never logged verbatim.
- Secrets are handled as `[]byte`, zeroed after use; the qBittorrent
  client receives a **password provider function** (fetched per login
  attempt) instead of a retained password string, so no copy of the
  secret outlives a login beyond GC-managed remnants (documented
  residual, same trust boundary as ADR-0004).
- Lookup strips at most one trailing newline from stdout (the
  tool's exact newline behavior is pinned by a unit test against a
  fake `secret-tool` script).
- **Fail-closed degradation**, never a prompt loop and never a
  plaintext fallback: if `secret-tool` is missing, the collection is
  locked, or the item is absent while the profile requires
  credentials, the daemon reports the distinct connection status
  `secrets_unavailable` and performs no login attempts.
- The `password` field is **removed** from `service.json`; a config
  file that still carries a non-empty `password` fails load with an
  explicit migration error (local bypass setups are unaffected).
  `username` (non-secret) stays in the connection profile.

## CONSEQUENCES

- `libsecret` (secret-tool) becomes an explicit runtime dependency of
  the daemon — recorded in docs/PACKAGING.md and docs/DEVELOPMENT.md.
- On machines that keep Omarchy's passwordless default keyring, the
  keyring store is plaintext at rest (mode 0600, same protection as
  this machine's existing Chromium/gh CLI credentials). This is the
  Omarchy baseline and is documented honestly here rather than hidden;
  a user who password-protects their keyring gets real at-rest
  encryption and the daemon degrades truthfully (`secrets_unavailable`)
  when the collection is locked.
- Same-UID attackers remain inside the ADR-0004 trust boundary: they
  could read the keyring or ptrace the daemon regardless of this
  choice. Cross-UID protection is what 0600 + the Secret Service give.

## ALTERNATIVES

- **Keep the 0600 config file** — rejected: plaintext file is exactly
  what Phase 0.5 removes; also forces the file to exist for remote
  setups where the daemon should own writes atomically.
- **systemd user credentials (`LoadCredential=`)** — works (`--user`
  encryption verified without root), but rotation requires rewriting
  the encrypted credential file and restarting the service; clunky for
  a password editable from a panel. Noted as a future option if the
  Secret Service ever becomes unavailable.
- **Pure-Go D-Bus (godbus) or zalando/go-keyring** — both viable and
  cgo-free today; rejected as default because they add dependencies to
  a stdlib-only daemon for a single secret. Revisit if packaging
  `secret-tool` ever becomes a problem.
- **KWallet** — installed but not the active provider; talking to the
  standard `org.freedesktop.secrets` interface (which KWallet also
  implements) keeps any future provider swap transparent.

## EVIDENCE / SOURCES

- On-machine research 2026-09-19: `pacman -Qi gnome-keyring libsecret`,
  `busctl --user list | grep secrets`, keyring collection inventory +
  lock state, disposable store/lookup/clear round-trip from a
  `manager`-class session (cleaned up), `secret-tool --help` + man
  page (stdin semantics), Omarchy install scripts
  (`/usr/share/omarchy/install/login/sddm.sh`,
  `install/user/default-keyring.sh`), network-panel stdin-not-argv
  comment (`/usr/share/omarchy/shell/plugins/panels/network/Model.js`).
- docs/SECURITY.md (Phase 0 interim decision this ADR replaces).
