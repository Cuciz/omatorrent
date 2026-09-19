# Phase 0.5 — Remote qBittorrent + security hardening (EVIDENCE)

Branch `feat/phase05-remote-qbittorrent` (base `93e14c1`, issue #9).
This file records the Phase 0.5 evidence per docs/TESTING.md rules.

## FINALIZATION (2026-09-19)

- External final review: **APPROVE FOR MERGE, blockers: none**.
- **PR #10 MERGED** (normal merge commit, no squash/rebase) at
  approved head **`8f0b515d442906999dc60d6b3c4caef7930c9800`**;
  merge SHA **`6563bd443afd01f580584eaecdbdf7ae3265a35d`**.
- **Issue #9 CLOSED** by the merge.
- Phase 0.5 — remote qBittorrent + security hardening — is
  **COMPLETE** on `main`; no remaining blockers. Feature branch
  deleted (local + remote) after merge.
- Next milestone (binding product decision): **0.5.1 — SPROUT
  REBRAND** (public name "Sprout", tagline "Torrent client for
  Omarchy"; technical identifiers unchanged; see ROADMAP and
  docs/agent/HANDOFF.md). Phase 0.6 has NOT started.
Review verdicts are recorded verbatim below; the omatorrent-dev-harness
reviewer agents were not available in this session — reviews were run
by independent read-only agents with the same charters (noted per
verdict).

## Scope guard (binding)

OmaTorrent stays strictly torrent-focused: VPN monitoring REMOVED from
scope (owned by another Omarchy plugin), NAS administration and general
network monitoring out of scope (ROADMAP decision 2026-09-19). No VPN,
proxy-manager, tunnel-manager, NAS-admin, network-monitor, Transmission
or multi-instance code exists in this phase.

## Research matrix (docs/QBITTORRENT.md §"Phase 0.5 research")

| Area | Result | Class |
|---|---|---|
| Login contract 5.2 | 204 success / 401 bad credentials / 403 ban | CONFIRMED SOURCE (v5_2_x) + LIVE (third-party client observed 204) |
| Login contract ≤ 5.1 | 200 "Ok." / "Fails." / 403 | CONFIRMED SOURCE (v4_6_x..v5_1_x) |
| Session cookie | `QBT_SID_<port>` (5.2) / `SID` (≤5.1), HttpOnly, path=/, sliding expiry | CONFIRMED LIVE + SOURCE |
| Origin/Referer | requests carrying NEITHER are explicitly allowed 4.6→5.2 | CONFIRMED SOURCE + LIVE (cross-origin GET → 401) |
| Bans | 5 fails → 1 h, per resolved IP, in-memory | CONFIRMED SOURCE; live frugality proven (3-attempt sticky, no ban) |
| Reverse proxy | no server-side base path; official recipes strip the prefix → client supports base-path URLs | CONFIRMED SOURCE (wiki + routing regex) |
| HTTPS | one port/one protocol, no redirect; self-signed common | CONFIRMED SOURCE |
| Session/rid | logout/re-login destroys sync state; any stale rid → full_update | CONFIRMED SOURCE + LIVE |
| 401 vs 403 | 401 = CSRF/Host mismatch (pre-auth) or 5.2 bad creds; 403 = no/expired session or ban | CONFIRMED SOURCE + LIVE (401 cross-origin) |
| Version compat | stop/start ≥ 2.11.0; pause/resume removed in 5.0; add 202+JSON on 5.2 | CONFIRMED SOURCE + LIVE (journal 404s) |
| Secret storage | gnome-keyring = hard `omarchy` dependency; passwordless default keyring auto-unlocks (proven from a manager-class session); secret-tool has NO argv secret parameter | CONFIRMED LIVE (research 2026-09-19) |
| Settings UX | no plugin-settings manifest; native pattern = panel-inline edit mode; `Ui.TextField` has `password` | CONFIRMED (installed Omarchy inspection) |

## Degraded-state matrix (task A–O)

| # | Scenario | Result | Evidence |
|---|---|---|---|
| A | local qB normal | PASS | unit `TestMatrixA` + live: daemon on local profile, status connected, panel screenshot |
| B | remote HTTPS normal | PASS | unit `TestMatrixB` (full TOFU pin flow) + LIVE (pinned HTTPS remote connected, screenshots) |
| C | hostname unreachable | PASS | unit `TestMatrixC` (NXDOMAIN `.invalid`) |
| D | TCP refused | PASS | unit `TestMatrixD` (ECONNREFUSED) |
| E | auth required (no credentials) | PASS | unit `TestMatrixE` + LIVE (remote fixture with auth, anonymous test → auth_required, screenshot) |
| F | bad password | PASS | unit `TestMatrixF` (sticky after 3, zero further backend contact) + LIVE (fixture password changed → auth_failed, no ban — correct login still accepted afterwards, screenshot) |
| G | session expiry, reauth OK | PASS | unit `TestMatrixG` (403 once → one re-login → connected) |
| H | session expiry, reauth fails | PASS | unit `TestMatrixH` |
| I | invalid TLS certificate | PASS | unit `TestMatrixI` + LIVE (self-signed vs system trust → tls_untrusted, fingerprint == openssl's, screenshot) |
| J | hostname mismatch | PASS | unit `TestMatrixJ` (pin anchor validates chain, hostname fails → tls_hostname) |
| K | remote HTTP explicit warning | PASS | unit `TestMatrixK` + LIVE (no-ack test → insecure_http; acknowledged → activates, insecure=true flagged in UI, screenshot) |
| L | malformed base URL | PASS | unit `TestMatrixL` (scheme/userinfo/fragment/query/…) + profile_test matrix |
| M | WebAPI incompatible/unsupported | PASS (by design) | no hard version gate (documented adapter rule): old-WebAPI backends still sync, stop/start gated per mutation (existing mutate suites). Unit `TestMatrixM` pins the no-false-blocking behavior |
| N | backend switch A→B | PASS | unit `TestMatrixN` + syncer/mutator switch suites + LIVE local→remote→local switches (epochs 1..6) |
| O | daemon restart with remote profile | PASS | unit `TestMatrixO` (store + secret reload, fresh epoch, sync) |

## Live evidence log (2026-09-19, disposable fixture)

Disposable `qbittorrent-nox 5.2.3` (temp profile, LAN address
`192.168.1.141:8090`, temporary admin password, ZERO torrents, killed
and deleted afterwards; the user's real backend and torrents verified
untouched — 3 torrents before/after; failed-credential experiments ran
ONLY against the disposable fixture, never the real backend):

1. Policy: `connection.test` http-no-ack → `insecure_http`; ack+anon →
   `auth_required` (real 403-no-credentials); ack+credentials → ok.
2. Remote HTTP activated: `connection.configured` epoch 1; status
   connected, mode remote, insecure true; secret stored in the REAL
   Secret Service (`secret-tool lookup` round-trip).
3. Found-and-fixed (live): secret-tool store exited 2
   ("Invalid byte sequence in conversion input") — GLib label charset
   conversion under the daemon's minimal child env; fix: ASCII item
   label + locale passthrough (commit 1cfd2cb). This is exactly the
   fail-closed `secrets_unavailable` path working as designed.
4. TLS: system trust vs self-signed → `tls_untrusted` with
   `offered_fingerprint` == openssl's SHA-256 (cross-verified).
5. Pin flow: pinned test with correct password → ok; activation with
   WRONG stored secret → sticky `auth_failed` after exactly 3 login
   attempts (30 s/60 s ladder); fixture NOT banned (subsequent correct
   login → 204).
6. Recovery: `replace` with correct password → connected remote HTTPS
   (pinned), screenshots (panel + dashboard).
7. Certificate rotation → pin mismatch → `tls_untrusted` (no
   downgrade), screenshot.
8. Restore: configured back to `http://127.0.0.1:8080` with
   `secret_action: delete`; keyring item removed (lookup empty);
   local torrents intact; fixture killed, temp files deleted.

Screenshots (docs/screenshots/): `phase05-panel-local.png`,
`phase05-panel-remote-insecure.png`, `phase05-panel-tls-untrusted.png`,
`phase05-panel-auth-required.png`, `phase05-panel-auth-failed.png`,
`phase05-panel-remote-https.png`, `phase05-panel-pin-mismatch.png`,
`phase05-dashboard-remote.png`, `phase05-dashboard-remote-https.png`.

NOT OBSERVABLE in the live shell: the settings FORM's interactive
interior (gear click-through, typing, button presses) — no synthetic
mouse input is available on this machine (wtype is keyboard-only);
every daemon state the form drives WAS observed live and is listed
above, and the form's QML was deployed, plugin-validated and loaded by
the running shell without errors.

## Performance (§29; localhost fixtures — CPU/auth/TLS-setup only, NO
WAN latency claims)

- Authenticated cycle (login + maindata + 2 version probes), local
  HTTP fixture: **~1.41 ms/cycle** (`BenchmarkAuthCycleLocalHTTP`,
  200×, i5-1334U).
- Same over TLS (pinned self-signed, keep-alive):
  **~1.24 ms/cycle** (`BenchmarkAuthCycleLocalTLS`) — handshake
  amortized by connection reuse; TLS adds no measurable per-cycle CPU.
- `connection.test` path: **~1.09 ms** (`BenchmarkConnectionTestPath`).
- State layer unchanged from 0.4 (no regression): full update at 1000
  torrents ~10.7 ms; 10-item delta at 1000 torrents ~0.54 ms
  (`internal/state` benchmarks).

## Security audit

- Journal grep for the fixture passwords across the whole phase: 0 hits
  (daemon logs carry classified codes and fixed detail strings only).
- Repo grep for the fixture passwords: 0 hits; `git diff --check`
  clean.
- `~/.config/omatorrent/connection.json`: 0600, no secret fields;
  secret lives only in the Secret Service and was deleted on restore.

## Validation suite (final run, 2026-09-19)

gofmt clean · `go vet ./...` clean · `go build ./...` clean ·
`go test -race -count=1 ./...` ALL PASS (config, connection, ipc,
mutate, qbittorrent, secrets, state) · `omarchy plugin validate` both
plugins PASS · `python3 tools/validate_harness.py` 82/82 ·
`bash tools/test_guard_hook.sh` 39/39 · `tools/test_quickshell.sh`:
OTQS-DONE with 10/10 mutation stages + 4/4 v1.4 connection stages
(including live idempotent configure: epoch 0→1, subscription stayed
truthful) · shell restarted, both plugins loaded, live UI observed.

## Reviews

(NOTE: the omatorrent-dev-harness reviewer agents were not available in
this ZCode session; independent read-only agents ran the same charters.)

- Architecture review (independent read-only agent, omarchy-reviewer
  charter): **PASS-WITH-FINDINGS** — all 12 boundary invariants
  verified PASS (one backend; daemon-owned connection/auth; no QML
  secrets/persistence; one canonical settings surface; epoch
  isolation; 0.3 mutation safety and 0.4 dashboard truthfulness
  intact; v1.0–v1.3 compatibility; no abstraction explosion; no
  Transmission/VPN/NAS code; docs consistency; clean dependency
  direction, zero external Go deps). Findings F1–F6 (logout-on-switch
  unimplemented; IPC/ADR doc drift: url/pin fields, schema field,
  Provider interface, epoch start) — ALL FIXED, see the ledger below.

- Security review (independent adversarial read-only agent): 
  **PASS-WITH-FINDINGS** — 15-point checklist: 13 PASS, 2
  PASS-with-finding. No secret reaches logs/IPC responses/QML
  persistence/argv/any file; TLS fail-closed everywhere; redirects
  structurally impossible; URL validation resisted every probed
  bypass. Findings F1–F9 (three MEDIUM concurrency/ordering issues in
  the switch path, one LOW pacing gap, five INFO) — ALL ADDRESSED, see
  the ledger.

- QA review (independent runtime-verifier agent, reproduction): 
  **PASS 14/14** — build/vet/race-suite clean; live daemon v1.4
  round-trips; own remote fixture probe (stateless test verified,
  unroutable-HTTP policy refused with zero network wait); matrix A–O
  unit suites, redirect/config/switch suites all PASS; full
  quickshell smoke OTQS-DONE (10/10 mutations + 4/4 connection
  stages); plugin validation with negative control; harness 82/82;
  guard 39/39; journal secret grep 0 hits; connection.json 0600 with
  no secret fields.

### Review-fix ledger (all implemented + regression-tested; full suite
and live smoke re-run green afterwards)

| Finding | Fix |
|---|---|
| Sec F1 (MEDIUM): two-phase switch let a submission validate on backend A and register on B | Mutator drain: `BeginSwitch` refuses in-flight AND rejects new registrations (`busy`) until `CommitSwap`/`AbortSwitch`; Configure drains BEFORE the secret op; syncer switches first (its state degrades instantly, so straddling submissions are refused `backend_unavailable`), mutator commits second (`TestDrainRejectsNewSubmissions`, `TestAbortSwitchReleasesDrain`) |
| Sec F2 (MEDIUM): secret ops not rolled back on later failure; stale has_secret | Configure snapshots the previous secret and restores it (and has_secret) on ANY post-op failure via `fail()`/`rollbackSecret` |
| Sec F3 (MEDIUM): publish race could resurrect old-backend rows after a switch | Cycle deltas and switch removals now publish UNDER the syncer lock (`publishLocked`, lock order s.mu→subsMu preserved) — a switch can never reorder around a cycle's delta |
| Sec F4 (LOW): credential-bearing connection.test logins unthrottled | One login-carrying test per 5 s window per daemon; excess refused with a fixed detail, zero backend contact (`TestConnectionTestLoginPacing`) |
| Sec F5 (INFO): mid-session re-login rejects not counted toward sticky | The maindata-error path now runs `noteAuthFailure` (≤4 total attempts, still under the 5-attempt ban) |
| Sec F6 (INFO): detail could reflect ~10 chars of input | Validation errors are fixed strings now (no scheme token / rune echo) |
| Sec F7 (INFO): read path followed symlinked profile directories | `refuseSymlinkedDir` component walk on load; stale comment fixed (`TestStoreRejectsSymlinkedDirectoryRead`) |
| Sec F8 (INFO): abandoned settings form retained the typed password | Closing settings clears `passText` |
| Sec F9 (INFO): cert cache unbounded bytes; unused stored-secret fetch | 64 KiB per-certificate cache cap; stored secret fetched only when a username is present and wiped after use |
| Arch F1 (MEDIUM): ADR-promised logout-on-switch unimplemented | Configure fire-and-forget best-effort `auth/logout` on the superseded client (2 s budget, no credentials in request); startup client attached in main (`TestSwitchLogsOutOldSession`) |
| Arch F2/F3/F4 (doc drift) | IPC.md status example now shows url/pin/epoch-starts-at-0 and detail-omitempty; ADR-0008 schema-field claim corrected; ADR-0009 Provider interface + ASCII-label rationale updated; post-review amendments recorded in ADR-0008 |

## External review round 2 (post-7d97c01, addressed)

The external review of head `7d97c01` found three blockers and one
hardening gap; all are fixed with targeted regression tests
(`internal/connection/policy_rollback_test.go`) and the full suite +
smoke re-run green:

1. **Startup HTTP-policy bypass** — `Profile.Validate` previously did
   not judge the HTTP policy, so a hand-edited/stale `connection.json`
   with remote HTTP and `allow_insecure_http:false` was accepted on
   daemon restart. FIX: the policy now lives INSIDE `Profile.Validate`,
   which every activation path runs (persisted load, service.json
   fallback, daemon-side save, client build) — startup is exactly as
   strict as configure. `connection.status`'s `insecure` is now the
   FACTUAL transport state (`non-loopback && http`), independent of
   consent. Tests: policy matrix (loopback/acked-remote/un-acked-
   remote/https), forged-raw-bytes load refusal, factual-status.
2. **Rollback target stale after multi-switch** — a long-lived
   `prevProfile` field made a failed B→C restore A. FIX: every
   Configure transaction snapshots the persisted profile as RAW BYTES
   at its start (`ReadStoreRaw`, exact restore via `SaveStoreRaw`;
   unpersisted fallback restores to no-file; an unreadable store
   refuses the transaction up front). Tests: A→B ok, B→C
   `storage_error` → runtime B, disk B, secret B, restart loads B, A
   never resurrected; byte-exact restore incl. absence.
3. **Secret snapshot error ambiguity** — the previous-secret `Get`
   error was ignored (a provider hiccup read as "absent", and `keep`
   transactions could roll back onto a live secret). FIX: `keep` never
   touches the provider; `replace`/`delete` REQUIRE a recoverable
   snapshot before any mutation (unusable snapshot ⇒ rejected
   `secrets_unavailable`, zero changes); a provider error is never
   "absent"; failed restorations keep the original rejection code and
   are logged secret-free (documented: no separate rollback-failure
   IPC code — the mismatch surfaces truthfully via status). Tests: the
   full six-case matrix incl. restore-of-absence and a failing
   restoration.
4. **Percent-encoded path attacks** — `/%2e%2e`, `/%2f`, `/%5c`,
   double-encoded forms were accepted by the escaped-path rules. FIX:
   base paths reject ANY percent-encoding (ambiguity rule: a proxy may
   decode/normalize before routing; the accepted prefix must remain
   the same canonical prefix under any ordinary decoding). Tests:
   17-case attack matrix + plain-prefix acceptance.

### Re-reviews after the round-2 fixes (focused on the actual diff)

- Security re-review (adversarial, executed the named tests plus its
  own out-of-tree probes of ValidateURL/normalizePath): **PASS** — no
  bypass found in any probe area: policy equivalence (A), factual
  insecure (B), rollback correctness (C), secret handling (D), path
  canonicalization (E — probed beyond the committed matrix incl. raw
  control bytes, `/./`, trailing `/..`, malformed escapes), round-1
  guarantees (F — none weakened). Alternative loopback spellings
  verified to fail toward the strict class; four INFO notes, none
  requiring change.
- QA re-review (independent reproduction): **PASS 9/9** — full race
  suite, all seven blocker regressions, live daemon v1.4 status
  (factual insecure), wire encoder tests, full smoke (10/10 + 4/4,
  OTQS-DONE), plugin validation with negative control, harness 82/82,
  guard 39/39, journal secret grep 0 over a non-empty window, git
  hygiene, and confirmation that round 2 touched no QML (visual
  re-validation correctly not repeated).
- Architecture re-review: **PASS-WITH-FINDINGS** — all seven requested
  points verified; F1 MEDIUM (concurrent Configure transactions could
  snapshot before exclusivity, resurrecting the stale-rollback class
  through concurrency) + five doc/hygiene findings. ALL FIXED:
  - F1: Configure now takes the mutator drain BEFORE its rollback
    snapshots (a concurrent Configure gets mutations_pending or runs
    strictly after the commit; snapshots are always current). Pinned
    by `TestConfigureExclusiveUnderDrain` (refused with zero changes)
    and `TestConcurrentConfiguresSerialize` (16 overlapping
    transactions; runtime always equals the persisted commit), run
    under -race.
  - F2: ADR-0008 §2/§3 amended (path canonicalization bullet incl.
    fail-closed loopback spelling; startup-equivalence and
    factual-reporting bullets).
  - F3: the HTTP-policy predicate is ONE function (`httpPolicyError`)
    shared by Profile.Validate, connection.test and
    connection.configure.
  - F4: IPC.md corrected — `connection.status` always carries `detail`
    (only the test response omits it when empty).
  - F5: `openProfileForRead` is the single shared read path for
    LoadStore and ReadStoreRaw (no check drift).
  - F6: dead assignment removed.

## Final concurrency correction (external review: REQUEST CHANGES, 1 blocker)

The external review of head `0b2ed7f` found that the PRODUCTION code
still took its rollback snapshots (and state-dependent TLS/pin
resolution) BEFORE acquiring the configure drain — despite commit
0b2ed7f, PHASE05.md and the PR body all claiming "exclusivity before
snapshots". That claim was wrong: the reorder patch in the 0b2ed7f
authoring session was lost to a failed edit script before its file
write, and the tests added alongside it passed vacuously w.r.t. the
ordering (`TestConfigureExclusiveUnderDrain` refutes regardless of
snapshot placement; `TestConcurrentConfiguresSerialize`'s invariant
survives stale rollbacks). This is recorded plainly: the earlier
evidence statements were incorrect, and the deterministic regression
below is the proof that actually matters.

**Deterministic regression** (`TestDeterministicStaleSnapshotConcurrency`,
`internal/connection/concurrency_regression_test.go`): a gating
provider pauses T2 by channel exactly inside its secret snapshot —
pre-BeginSwitch on the old ordering, drain-holding on the fixed one —
while T1 runs a full Configure. No sleeps, no scheduler luck.

- Result on the OLD head `0b2ed7f` (executed 2026-09-19):
  **FAIL** — "T1 committed B, but after T2's failure the persisted
  profile is [A] — a committed activation was rolled back". The
  historical bug reproduced deterministically. (An earlier
  collision-based draft PASSED on old code because the collision that
  blocked the forward write also blocked the rollback write; the
  final test uses a test-only post-persistence build-failure hook so
  the rollback really executes.)
- Result on the fixed code: **PASS** — T1 is refused
  (`mutations_pending`) and observationally inert (store byte-identical,
  zero provider calls, no epoch/profile change; the `readStoreRawFn`
  snapshot spy is asserted separately by the strengthened
  `TestConfigureExclusiveUnderDrain`); T2 then fails after its mutation point and restores
  its OWN snapshot; final state is A in runtime, on disk, in the
  secret pairing and after restart. The suite was looped 10× under
  `-race` (green) after making the test harness deterministically wait
  for the Manager's background presence probe (a counter-race flake
  was found and fixed, not ignored).

**Corrected Configure ordering** (now literally true in production,
`internal/connection/manager.go`):

1. PURE request validation — URL, HTTP policy, secret-action enum,
   replace-requires-password, TLS token + pin SYNTAX
   (`validateTLSRequest`, no Manager-state reads). Fully inert
   rejections.
2. `BeginSwitch` — exclusivity; a `drainHeld` guard with a deferred
   `AbortSwitch` releases the drain on EVERY post-acquisition
   failure; `CommitSwap` clears the guard on success (no
   double-abort).
3. Raw-bytes profile snapshot (`readStoreRawFn`).
4. Secret snapshot (replace/delete only; `keep` never touches the
   provider).
5. State-dependent TLS resolution (`resolveTLSOptions`: offered-cert
   cache + active-pin reuse — reads CURRENT Manager state, therefore
   only under exclusivity; a superseded profile's pin cannot supply
   trust material).
6. Build `next` profile; apply the secret action (`secretMutated`).
7. `SaveStore(next)` (`storeMutated`).
8. Build-failure hook (test-only) + client build.
9. Activation entirely under the drain: syncer switch → Manager
   state/epoch/client commit → `CommitSwap` (releases the drain) →
   `drainHeld = false` → best-effort old-client logout. (End-of-
   transaction ordering tightened after the final architecture
   re-review: the Manager's in-memory state is committed BEFORE the
   drain releases, so a preempted winner can never overwrite a newer
   committed transaction's state.)

Rollback runs only for what actually mutated (`secretMutated` /
`storeMutated`): a failed Store/Delete is treated as atomic (nothing
restored); a SaveStore failure restores only the secret; a post-persist
failure restores secret AND raw store bytes.

### Final re-review verdicts (on the actual diff, all seven mandatory
questions answered)

- **Architecture: PASS-WITH-FINDINGS** — line-by-line source trace
  confirmed BeginSwitch (manager.go:582) precedes the raw store
  snapshot (:619), the secret snapshot (:635) and state-dependent TLS
  resolution (:650); Phase A verified genuinely state-free; all eight
  post-BeginSwitch failure returns covered by the deferred guard;
  rollback flags exactly timed. Seven mandatory questions: all
  favorable. Findings: F1 MEDIUM — the winner's in-MEMORY state was
  committed AFTER the drain released (same stale-state class, opposite
  end of the transaction) → FIXED (activation now entirely under the
  drain; CommitSwap is the literal last step); F2 doc-attribution →
  FIXED; F3 INFO (failed-Store-atomicity assumption, documented).
- **Security (adversarial): PASS-WITH-FINDINGS** — all seven mandatory
  questions favorable; exhaustive interleaving analysis (winner fails
  at each phase), stale-trust analysis (offered cache is
  fingerprint-exact; the pin is an identity lock that concurrent tests
  cannot widen), seam-pollution analysis (test-only, no production
  path), round-2 guarantees re-verified. Independently reproduced the
  old-ordering failures in a detached worktree (all three exclusivity
  tests FAIL on the 0b2ed7f ordering, PASS on the fix). Findings: F1
  MEDIUM = the same in-memory window (verified fixed by the follow-up
  commit; exclusivity suite re-run green there); F2 LOW pre-existing
  pacing check-then-set race → FIXED (check-and-stamp now one critical
  section); F3–F5 INFO (documented assumptions; one unreproduced
  mutate-suite flake under parallel load, package untouched by this
  diff — noted as a watch item, not chased in this task).
- **QA (independent reproduction): PASS 9/9** — full race suite,
  deterministic regression (and the old-head failure reproduced
  VERBATIM in a clean worktree: "STALE ROLLBACK: T1 committed B, but
  after T2's failure the persisted profile is … — a committed
  activation was rolled back"), exclusivity + pin suites, all eight
  round-2 suites, source-line ordering proof (582 < 619 < 635 < 650),
  smoke OTQS-DONE (10/10 + 4/4), plugins ×2, harness 82/82, guard
  39/39, journal secret grep 0, repo scans clean, live daemon healthy.

New/changed tests: `TestDeterministicStaleSnapshotConcurrency`
(the proof), `TestConfigureExclusiveUnderDrain` (strengthened with the
`readStoreRawFn` spy + provider counters + epoch/profile assertions),
`TestConcurrentConfiguresSerialize` (retained),
`TestPinStateResolvedUnderExclusivity` (§11: active-pin reuse resolved
under exclusivity; concurrent pin-reuse refused inertly; superseded
pins stay `pin_unknown`; TOFU unchanged),
`TestConfigureRollbackRestoresCurrentNotStale` (now fails T2 after
persistence via the hook so BOTH rollback writes really land).

## Known limitations

- `connection.test`/`connection.configure` carry the password once per
  user action over the 0600 local socket (ADR-0004 same-UID trust
  boundary); the parsed frame copy is unreachable after the handler —
  documented residual, not re-architected.
- Bearer API-key auth (≥ 5.2.0) is recorded as a future profile option
  (ADR-0008 alternatives); cookie login is the 0.5 mechanism.
- CA-bundle TLS mode is file-only (`connection.json`), not exposed over
  IPC; the settings surface offers system trust + TOFU pin.
- `epoch` is per-daemon-process; clients must not assume global
  monotonicity across daemon restarts (documented in IPC.md).
- Base-path URLs require a prefix-stripping reverse proxy (official
  recipes); qBittorrent itself serves at root only.
- Live auth-failure/TLS evidence used a loopback-adjacent LAN fixture;
  genuine WAN latency/behavior is untested (benchmarks are localhost).
