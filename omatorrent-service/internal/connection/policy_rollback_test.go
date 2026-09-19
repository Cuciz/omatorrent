package connection

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/mutate"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/secrets"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// ============================================================
// Blocker 1: startup/load enforces the same HTTP policy as
// configure; `insecure` is factual transport state.
// ============================================================

func TestStartupHTTPPolicyMatrix(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		ack      bool
		accepted bool
		insecure bool
	}{
		{"loopback http, no ack", "http://127.0.0.1:8080", false, true, false},
		{"remote http, acked", "http://192.0.2.10:8080", true, true, true},
		{"remote http, NOT acked", "http://192.0.2.10:8080", false, false, false},
		{"remote https, no ack", "https://192.0.2.10:8443", false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Profile{URL: c.url, TLSMode: TLSSystem, AllowInsecureHTTP: c.ack}
			ep, err := p.Validate()
			if c.accepted {
				if err != nil {
					t.Fatalf("Validate rejected: %v", err)
				}
				if ep.InsecureTransport() != c.insecure {
					t.Fatalf("InsecureTransport = %v, want %v", ep.InsecureTransport(), c.insecure)
				}
			} else if err == nil {
				t.Fatal("Validate accepted a policy-violating profile")
			}

			// The SAME policy holds for the persisted store: a forged or
			// stale connection.json cannot bypass it on load.
			path := profilePath(t)
			if err := SaveStore(path, p); err != nil {
				if c.accepted {
					t.Fatalf("save accepted profile rejected: %v", err)
				}
				return // rejected at save — good enough (write path also validates)
			}
			if _, _, err := LoadStore(path); c.accepted && err != nil {
				t.Fatalf("load accepted profile rejected: %v", err)
			} else if !c.accepted && err == nil {
				t.Fatal("LoadStore accepted a policy-violating persisted profile")
			}
			if _, _, err := LoadActive(path, DefaultProfile()); c.accepted && err != nil {
				t.Fatalf("LoadActive rejected accepted profile: %v", err)
			} else if !c.accepted && err == nil {
				t.Fatal("LoadActive accepted a policy-violating persisted profile")
			}
		})
	}
}

// A hand-forged connection.json (raw bytes, never passed through
// SaveStore) with remote HTTP and no acknowledgement is refused at
// load — the exact external-review bypass scenario.
func TestForgedRemoteHTTPProfileRejectedOnLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "omatorrent"), 0o700)
	path := filepath.Join(dir, "omatorrent", "connection.json")
	forged := `{"url":"http://192.168.1.40:8080","tls_mode":"system","allow_insecure_http":false}`
	if err := os.WriteFile(path, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadStore(path); err == nil {
		t.Fatal("forged un-acked remote-HTTP profile accepted at load")
	}
	if _, _, err := LoadActive(path, DefaultProfile()); err == nil {
		t.Fatal("forged un-acked remote-HTTP profile accepted at startup")
	}
	// The same bytes WITH the acknowledgement load fine.
	acked := strings.Replace(forged, `"allow_insecure_http":false`, `"allow_insecure_http":true`, 1)
	os.WriteFile(path, []byte(acked), 0o600)
	if p, _, err := LoadActive(path, DefaultProfile()); err != nil || !p.AllowInsecureHTTP {
		t.Fatalf("acked remote-HTTP profile rejected: %v", err)
	}
}

// The status surface reports the FACTUAL transport: consent never
// launders reality.
func TestStatusInsecureIsFactual(t *testing.T) {
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})
	// Remote HTTP, acknowledged: insecure MUST be true (reality).
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: "http://192.0.2.10:8080", AllowInsecureHTTP: true, TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	if st := h.mgr.Status(); !st.Insecure || st.Transport != "http" {
		t.Fatalf("acked remote HTTP status = %+v, want insecure=true", st)
	}
	// Loopback HTTP: never insecure.
	cfg = h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: "http://127.0.0.1:8080", TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("restore local = %+v", cfg)
	}
	if st := h.mgr.Status(); st.Insecure {
		t.Fatalf("loopback status = %+v, want insecure=false", st)
	}
}

// ============================================================
// Blocker 2: the rollback target is the CURRENT active state,
// snapshotted per transaction — B→C failure restores B, never A.
// ============================================================

func TestConfigureRollbackRestoresCurrentNotStale(t *testing.T) {
	fxA := &qbFixture{torrents: 1, appVersion: "vA", apiVersion: "2.15.1"}
	fxB := &qbFixture{torrents: 2, appVersion: "vB", apiVersion: "2.15.1"}
	fxC := &qbFixture{torrents: 3, appVersion: "vC", apiVersion: "2.15.1"}
	tsA := httptest.NewServer(fxA.handler())
	defer tsA.Close()
	tsB := httptest.NewServer(fxB.handler())
	defer tsB.Close()
	tsC := httptest.NewServer(fxC.handler())
	defer tsC.Close()

	h := newHarness(t, localProfile(tsA.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(tsA.URL, "", "")
	})

	// A → B succeeds (secret B stored; B persisted).
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: tsB.URL, Username: "b", Password: []byte("secretB"), TLSMode: TLSSystem}, SecretAction: "replace"}); !cfg.OK {
		t.Fatalf("A->B = %+v", cfg)
	}
	// B → C fails AFTER persistence: SaveStore(C) lands, then the
	// test-only build hook aborts the transaction, so BOTH rollback
	// writes (secret restore + raw store restore) really execute.
	buildFailureHook = func(next Profile) error { return errPostPersist }
	defer func() { buildFailureHook = nil }()
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: tsC.URL, Username: "c", Password: []byte("secretC"), TLSMode: TLSSystem}, SecretAction: "replace"})
	if cfg.OK || cfg.Rejection != RejectInvalidURL {
		t.Fatalf("B->C = %+v, want the injected post-persistence rejection", cfg)
	}
	buildFailureHook = nil

	// Runtime remains B; the persisted file is B again; the secret is
	// B's again; A is nowhere.
	if p := h.mgr.Profile(); p.URL != tsB.URL || p.Username != "b" {
		t.Fatalf("runtime profile after failed switch = %+v, want B", p)
	}
	persisted, exists, err := LoadStore(h.store)
	if err != nil || !exists {
		t.Fatalf("persisted profile unreadable: exists=%v err=%v", exists, err)
	}
	if persisted.URL != tsB.URL || persisted.Username != "b" {
		t.Fatalf("persisted profile = %+v, want B (stale A must never be resurrected)", persisted)
	}
	if string(h.prov.SecretValue()) != "secretB" {
		t.Fatalf("secret = %q, want B's restored", h.prov.SecretValue())
	}

	// Restart loads B — not A, not C.
	syncer2 := state.New(mustClient(t, persisted, h.prov), state.Options{}, quietLog())
	mut2 := h.mutator // fresh mutator not required for LoadActive; reuse harness helper
	_ = mut2
	mgr2, err := NewManager(h.store, DefaultProfile(), h.prov, syncer2, syncer2, h.mutator, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if p := mgr2.Profile(); p.URL != tsB.URL || p.Username != "b" {
		t.Fatalf("post-restart profile = %+v, want B", p)
	}
}

// Byte-exact post-persistence restore: the transaction snapshot is
// restored verbatim (not re-marshaled), so even formatting survives.
func TestRollbackRestoresExactBytes(t *testing.T) {
	store := profilePath(t)
	if err := SaveStore(store, Profile{URL: "http://127.0.0.1:8081", TLSMode: TLSSystem, Username: "bb"}); err != nil {
		t.Fatal(err)
	}
	before, existed, err := ReadStoreRaw(store)
	if err != nil || !existed {
		t.Fatalf("snapshot: existed=%v err=%v", existed, err)
	}
	// Persist a different valid profile (simulating the switch's write),
	// then roll the bytes back.
	if err := SaveStore(store, Profile{URL: "http://127.0.0.1:8082", TLSMode: TLSSystem}); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreRaw(store, before); err != nil {
		t.Fatal(err)
	}
	after, _, _ := ReadStoreRaw(store)
	if string(after) != string(before) {
		t.Fatal("byte-exact restore failed")
	}
	// Absence restores to absence.
	if err := removeStore(store); err != nil {
		t.Fatal(err)
	}
	if err := SaveStore(store, Profile{URL: "http://127.0.0.1:8083", TLSMode: TLSSystem}); err != nil {
		t.Fatal(err)
	}
	if err := removeStore(store); err != nil { // rollback-of-absence
		t.Fatal(err)
	}
	if _, exists, _ := ReadStoreRaw(store); exists {
		t.Fatal("absence not restored")
	}
}

// ============================================================
// Blocker 3: secret snapshot error handling.
// ============================================================

func TestSecretRollbackMatrix(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	newH := func() *harness {
		return newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
			return qbittorrent.New(ts.URL, "", "")
		})
	}
	collide := func(h *harness) string {
		dir := filepath.Dir(h.store)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, ".connection.json.tmp")
		if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return tmp
	}

	t.Run("keep never touches provider even on later failure", func(t *testing.T) {
		h := newH()
		h.prov.SetSecret([]byte("existing"))
		// Let the Manager's one-shot background presence probe finish so
		// the call counters baseline cleanly (it is NOT part of the
		// configure transaction).
		time.Sleep(150 * time.Millisecond)
		h.prov.SetUnavailable(true) // even an unusable provider must not matter for keep
		g, d := h.prov.GetCalls, h.prov.DelCalls
		tmp := collide(h)
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, TLSMode: TLSSystem}, SecretAction: "keep"})
		os.Remove(tmp)
		if cfg.OK || cfg.Rejection != RejectStorageError {
			t.Fatalf("configure = %+v, want storage_error", cfg)
		}
		if h.prov.GetCalls != g || h.prov.StoreCalls != 0 || h.prov.DelCalls != d {
			t.Fatal("keep touched the secret provider")
		}
		h.prov.SetUnavailable(false)
		if string(h.prov.SecretValue()) != "existing" {
			t.Fatal("existing secret damaged by a keep transaction")
		}
	})

	t.Run("replace with unusable snapshot rejected before store", func(t *testing.T) {
		h := newH()
		h.prov.SetUnavailable(true)
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, Username: "u", Password: []byte("new"), TLSMode: TLSSystem}, SecretAction: "replace"})
		if cfg.OK || cfg.Rejection != RejectSecretsMissing {
			t.Fatalf("configure = %+v, want secrets_unavailable", cfg)
		}
		if h.prov.StoreCalls != 0 {
			t.Fatal("Store called despite unusable snapshot")
		}
		if _, exists, err := ReadStoreRaw(h.store); exists || err != nil {
			t.Fatal("store modified despite snapshot rejection")
		}
	})

	t.Run("delete with unusable snapshot rejected before delete", func(t *testing.T) {
		h := newH()
		h.prov.SetSecret([]byte("mine"))
		h.prov.SetUnavailable(true)
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, Username: "u", TLSMode: TLSSystem}, SecretAction: "delete"})
		if cfg.OK || cfg.Rejection != RejectSecretsMissing {
			t.Fatalf("configure = %+v, want secrets_unavailable", cfg)
		}
		if h.prov.DelCalls != 0 {
			t.Fatal("Delete called despite unusable snapshot")
		}
		h.prov.SetUnavailable(false)
		if string(h.prov.SecretValue()) != "mine" {
			t.Fatal("secret deleted despite rejected operation")
		}
	})

	t.Run("replace then failure restores previous value", func(t *testing.T) {
		h := newH()
		h.prov.SetSecret([]byte("old"))
		tmp := collide(h)
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, Username: "u", Password: []byte("new"), TLSMode: TLSSystem}, SecretAction: "replace"})
		os.Remove(tmp)
		if cfg.OK {
			t.Fatal("configure unexpectedly succeeded")
		}
		if string(h.prov.SecretValue()) != "old" {
			t.Fatalf("secret = %q, want restored old", h.prov.SecretValue())
		}
	})

	t.Run("replace then failure restores previous ABSENCE", func(t *testing.T) {
		h := newH() // no secret stored
		tmp := collide(h)
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, Username: "u", Password: []byte("new"), TLSMode: TLSSystem}, SecretAction: "replace"})
		os.Remove(tmp)
		if cfg.OK {
			t.Fatal("configure unexpectedly succeeded")
		}
		if h.prov.SecretValue() != nil {
			t.Fatal("absence not restored after failed replace")
		}
	})

	t.Run("restore failure keeps original rejection, no panic", func(t *testing.T) {
		// A provider whose SECOND Store (the restore attempt) fails.
		// Built up front — production never swaps a live Manager's
		// provider.
		prov := &secrets.Fake{}
		prov.SetSecret([]byte("old"))
		wrap := &failingRestoreProvider{Provider: prov}
		client, err := qbittorrent.New(ts.URL, "", "")
		if err != nil {
			t.Fatal(err)
		}
		syncer := state.New(client, state.Options{}, quietLog())
		mut := mutate.New(client, syncer, mutate.Options{ReconcileWindow: 60 * time.Millisecond}, quietLog())
		store := profilePath(t)
		mgr, err := NewManager(store, DefaultProfile(), wrap, syncer, syncer, mut, quietLog())
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(store), 0o700)
		tmp := filepath.Join(filepath.Dir(store), ".connection.json.tmp")
		if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmp)
		cfg := mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, Username: "u", Password: []byte("new"), TLSMode: TLSSystem}, SecretAction: "replace"})
		if cfg.OK || cfg.Rejection != RejectStorageError {
			t.Fatalf("configure = %+v, want the ORIGINAL rejection preserved", cfg)
		}
		if wrap.stores != 2 {
			t.Fatalf("store calls = %d, want 1 mutation + 1 failed restore", wrap.stores)
		}
		// Documented residual: the restore failed; the provider holds
		// the new value against the rolled-back profile — the daemon
		// reports the mismatch truthfully (auth_failed downstream)
		// rather than pretending success.
	})
}

// failingRestoreProvider fails the SECOND Store call (the restore).
type failingRestoreProvider struct {
	secrets.Provider
	stores int
}

func (f *failingRestoreProvider) Store(ctx context.Context, secret []byte) error {
	f.stores++
	if f.stores == 2 {
		return secrets.ErrUnavailable // the restore attempt
	}
	return f.Provider.Store(ctx, secret)
}

// ============================================================
// Blocker 4: percent-encoded path attacks.
// ============================================================

func TestValidateURLEncodedPathAttacks(t *testing.T) {
	attacks := []string{
		"https://h/%2e",
		"https://h/%2e%2e",
		"https://h/qbt/%2e%2e/admin",
		"https://h/%2E%2E/",
		"https://h/%2E/",                // uppercase single dot
		"https://h/%2f",                 // encoded separator at root
		"https://h/qbt%2fqbt2",          // encoded separator INSIDE a segment
		"https://h/qbt/%2F",             // uppercase encoded separator
		"https://h/%5c",                 // encoded backslash
		"https://h/qbt%5Csub",           // encoded backslash inside segment
		"https://h/%252e%252e",          // double-encoded dot-dot
		"https://h/qbt/%25%32%66",       // fully double-encoded separator
		"https://h/%u002e",              // invalid escape form
		"https://h/qbt%",                // truncated escape
		"https://h/qb%74",               // innocuous encoding — still rejected (ambiguity rule)
		"https://h/%2e%2e%2f",           // mixed dot-dot + separator
		"https://h/qbt/%2e%2e%5c%2e%2e", // dot-dot + encoded backslash mix
	}
	for _, raw := range attacks {
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) accepted an encoded-path attack", raw)
		}
	}
	// Plain prefixes keep working.
	for _, ok := range []string{"https://h/qbt", "https://h/qbt/", "https://h/qbt/sub"} {
		if _, err := ValidateURL(ok); err != nil {
			t.Errorf("ValidateURL(%q) rejected a plain prefix: %v", ok, err)
		}
	}
}

// Alternative loopback spellings (decimal/hex/octal/short IP forms,
// zoned IPv6, dotted localhost) must classify as REMOTE — the strict
// class. Go's resolver may dial some of them as 127.0.0.1, so the
// exemption is reserved for the literal forms only; ambiguity always
// fails toward remote (HTTPS/acknowledgement required).
func TestLoopbackClassificationFailsClosed(t *testing.T) {
	for _, host := range []string{"2130706433", "0x7f.1", "127.1", "0177.0.0.1", "[::1%25eth0]", "LOCALHOST.", "localhost.example"} {
		ep, err := ValidateURL("http://" + host + ":8080")
		if err != nil {
			continue // rejected outright is even stricter
		}
		if ep.IsLoopback {
			t.Errorf("host %q classified loopback — must fail toward remote", host)
		}
	}
	for _, host := range []string{"127.0.0.1", "[::1]", "localhost"} {
		if ep, err := ValidateURL("http://" + host + ":8080"); err != nil || !ep.IsLoopback {
			t.Errorf("host %q lost its loopback classification", host)
		}
	}
}

// Architecture re-review F1: Configure takes exclusivity BEFORE its
// rollback snapshots. While a drain is open, a Configure is refused
// (mutations_pending) with NOTHING changed — a second transaction can
// never snapshot a state that predates another transaction's commit.
func TestConfigureExclusiveUnderDrain(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()
	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	// Simulate another transaction mid-drain.
	if err := h.mutator.BeginSwitch(); err != nil {
		t.Fatal(err)
	}
	before, _, _ := ReadStoreRaw(h.store)
	baseEpoch := h.mgr.Status().Epoch

	// Spy on the snapshot seam: a refused configure must not even read
	// the persisted profile.
	reads := 0
	readStoreRawFn = func(path string) ([]byte, bool, error) {
		reads++
		return ReadStoreRaw(path)
	}
	defer func() { readStoreRawFn = ReadStoreRaw }()

	g0, s0, d0 := h.prov.Counts()
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", Password: []byte("x"), TLSMode: TLSSystem}, SecretAction: "replace"})
	if cfg.OK || cfg.Rejection != RejectMutationsPending {
		t.Fatalf("configure under drain = %+v, want mutations_pending", cfg)
	}
	if reads != 0 {
		t.Fatalf("refused configure took %d profile snapshots (must be 0)", reads)
	}
	if g1, s1, d1 := h.prov.Counts(); g1 != g0 || s1 != s0 || d1 != d0 {
		t.Fatal("refused configure called the secret provider")
	}
	after, _, _ := ReadStoreRaw(h.store)
	if string(before) != string(after) {
		t.Fatal("store modified by a refused (drained) configure")
	}
	if e := h.mgr.Status().Epoch; e != baseEpoch {
		t.Fatalf("refused configure changed the epoch: %d -> %d", baseEpoch, e)
	}
	if u := h.mgr.Profile().URL; u != ts.URL {
		t.Fatalf("refused configure changed the active profile: %s", u)
	}
	h.mutator.AbortSwitch()
}

// §11: active-pin reuse resolves ONLY while exclusivity is held. With
// a pinned profile active and another transaction paused holding the
// drain, a concurrent Configure reusing the active pin is refused
// inertly; the paused transaction then commits with trust material
// derived from the CURRENT profile. A pin belonging to a superseded
// profile (cache empty) stays pin_unknown.
func TestPinStateResolvedUnderExclusivity(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()
	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})

	// Seed the offered-certificate cache with a REAL self-signed
	// certificate (a fake DER would fail PEM-anchor construction).
	_, der := selfSignedFor(t, "pin-concurrency.local")
	fp := qbittorrent.Fingerprint(der)
	h.mgr.cacheOffered(fp, der)

	// Activate a PINNED profile A (pin trust from the cache).
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSPin, Pin: fp}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("pin activation = %+v", cfg)
	}
	activePin := h.mgr.Profile().PinFingerprint
	if activePin != fp {
		t.Fatalf("active pin = %q", activePin)
	}

	// Pause T2 holding the drain (it blocks in its secret snapshot via
	// a gating provider; keep is not used so the snapshot runs).
	prov := &secrets.Fake{}
	prov.SetSecret([]byte("s"))
	gate := newGatingProvider(prov)
	h.mgr.secrets = gate // safe here: the presence probe already ran
	entered, release := gate.arm(gate.count() + 1)
	t2done := make(chan ConfigureResult, 1)
	go func() {
		t2done <- h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
			URL: ts.URL, TLSMode: TLSPin, Pin: activePin, Password: []byte("next")}, SecretAction: "replace"})
	}()
	<-entered // T2 holds the drain, paused at its snapshot.

	// Concurrent Configure reusing the ACTIVE pin: refused, inert.
	baseEpoch := h.mgr.Status().Epoch
	c2 := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSPin, Pin: activePin}, SecretAction: "keep"})
	if c2.OK || c2.Rejection != RejectMutationsPending {
		t.Fatalf("concurrent pin-reuse configure = %+v, want mutations_pending", c2)
	}
	if e := h.mgr.Status().Epoch; e != baseEpoch {
		t.Fatal("refused pin-reuse configure changed the epoch")
	}

	// Release T2: it commits, having resolved the active pin under
	// exclusivity.
	close(release)
	if r := <-t2done; !r.OK {
		t.Fatalf("T2 = %+v, want success with active-pin reuse", r)
	}
	if p := h.mgr.Profile(); p.TLSMode != TLSPin || p.PinFingerprint != activePin || p.PinCertPEM == "" {
		t.Fatalf("post-commit pinned profile = %+v", p)
	}

	// A pin from a SUPERSEDED profile cannot be resurrected once the
	// active profile moves on and the cache is empty: reusing the old
	// pin is pin_unknown (truthful TOFU).
	h.mgr.mu.Lock()
	h.mgr.offered = map[string][]byte{}
	h.mgr.mu.Unlock()
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSSystem}, SecretAction: "keep"}); !cfg.OK {
		t.Fatalf("switch to system = %+v", cfg)
	}
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSPin, Pin: fp}, SecretAction: "keep"}); cfg.OK || cfg.Rejection != RejectPinUnknown {
		t.Fatalf("superseded-pin reuse = %+v, want pin_unknown", cfg)
	}
}

// Overlapping Configure transactions serialize: whatever interleaving
// the race scheduler picks, the runtime profile always equals a
// COMMITTED profile and the persisted store always loads cleanly.
func TestConcurrentConfiguresSerialize(t *testing.T) {
	fixtures := make([]*qbFixture, 4)
	servers := make([]*httptest.Server, 4)
	urls := make([]string, 4)
	for i := range servers {
		fixtures[i] = &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
		servers[i] = httptest.NewServer(fixtures[i].handler())
		urls[i] = servers[i].URL
		defer servers[i].Close()
	}
	h := newHarness(t, localProfile(urls[0]), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(urls[0], "", "")
	})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
				URL: urls[n%len(urls)], TLSMode: TLSSystem}, SecretAction: "keep"})
		}(i)
	}
	wg.Wait()
	// Invariant: runtime == some committed profile == loadable store.
	persisted, exists, err := LoadStore(h.store)
	if err != nil || !exists {
		t.Fatalf("store unreadable after concurrent configures: exists=%v err=%v", exists, err)
	}
	if cur := h.mgr.Profile(); cur.URL != persisted.URL {
		t.Fatalf("runtime (%s) diverged from the persisted commit (%s)", cur.URL, persisted.URL)
	}
}

// errPostPersist is the deterministic post-persistence failure for
// the sequential rollback test.
var errPostPersist = errors.New("injected post-persistence failure")
