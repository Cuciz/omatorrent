package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
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

// qbFixture is a configurable qBittorrent-like fixture: optional
// credential enforcement, session cookie, maindata with N torrents,
// version strings, and scriptable failures.
type qbFixture struct {
	mu           sync.Mutex
	username     string
	password     string
	requireAuth  bool
	sessions     int     // server-side session count (cookie mints)
	torrents     int     // full-update torrent count
	appVersion   string  // served app/version
	apiVersion   string  // served app/webapiVersion
	maindataErr  int     // respond 403 to the next N maindata calls
	loginFail    bool    // login answers 401 (5.2 contract)
	maindataRIDs []int64 // rids received (cookie/session evidence)
}

func (f *qbFixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if f.loginFail ||
				(f.username != "" && (r.PostFormValue("username") != f.username || r.PostFormValue("password") != f.password)) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.sessions++
			http.SetCookie(w, &http.Cookie{Name: "QBT_SID_1", Value: fmt.Sprintf("sess-%d", f.sessions), Path: "/"})
			w.WriteHeader(http.StatusNoContent)
			return
		case "/api/v2/app/version":
			fmt.Fprint(w, f.appVersion)
			return
		case "/api/v2/app/webapiVersion":
			fmt.Fprint(w, f.apiVersion)
			return
		case "/api/v2/torrents/stop", "/api/v2/torrents/start", "/api/v2/torrents/pause", "/api/v2/torrents/resume", "/api/v2/torrents/delete", "/api/v2/torrents/add":
			w.WriteHeader(http.StatusOK)
			return
		case "/api/v2/sync/maindata":
			if f.requireAuth && len(r.Cookies()) == 0 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if f.maindataErr > 0 {
				f.maindataErr--
				w.WriteHeader(http.StatusForbidden)
				return
			}
			rid := r.URL.Query().Get("rid")
			var ridN int64
			fmt.Sscanf(rid, "%d", &ridN)
			f.maindataRIDs = append(f.maindataRIDs, ridN)
			torrents := map[string]json.RawMessage{}
			for i := 0; i < f.torrents; i++ {
				h := fmt.Sprintf("%040x", i+1)
				torrents[h] = json.RawMessage(fmt.Sprintf(`{"name":"T%d","state":"downloading","progress":0.5,"dlspeed":1,"upspeed":0,"eta":10,"ratio":1.0,"size":100,"completed":50}`, i))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"rid": ridN + 1, "full_update": true, "torrents": torrents,
				"server_state": map[string]any{"dl_info_speed": 0, "up_info_speed": 0, "connection_status": "connected"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// harness wires a Manager against real Syncer/Mutator instances.
type harness struct {
	t       *testing.T
	store   string
	prov    *secrets.Fake
	syncer  *state.Syncer
	mutator *mutate.Mutator
	mgr     *Manager
}

func newHarness(t *testing.T, fallback Profile, initial ClientBuilder) *harness {
	t.Helper()
	prov := &secrets.Fake{}
	client, err := initial()
	if err != nil {
		t.Fatal(err)
	}
	syncer := state.New(client, state.Options{}, quietLog())
	// Short reconcile window so ambiguous pendings settle quickly in
	// tests (production default is 10 s; behavior is identical).
	mutator := mutate.New(client, syncer, mutate.Options{ReconcileWindow: 60 * time.Millisecond}, quietLog())
	store := profilePath(t)
	mgr, err := NewManager(store, fallback, prov, syncer, syncer, mutator, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, store: store, prov: prov, syncer: syncer, mutator: mutator, mgr: mgr}
}

type ClientBuilder func() (*qbittorrent.Client, error)

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func localProfile(url string) Profile { return Profile{URL: url, TLSMode: TLSSystem} }

func ctx() context.Context { return context.Background() }

// ---- A. local qBittorrent, normal (the unchanged default experience) ----

func TestMatrixA_LocalNormal(t *testing.T) {
	fx := &qbFixture{torrents: 3, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	// Anonymous loopback: no credentials, no login, connected.
	driveCycles(t, h.syncer, 2)
	st := h.mgr.Status()
	if !st.Configured || st.Status != StatusConnected || st.Mode != "local" {
		t.Fatalf("status = %+v", st)
	}
	if st.Username != "" || st.HasSecret {
		t.Fatalf("local default must be credential-free: %+v", st)
	}
}

// ---- B. remote HTTPS normal, via the full trust/pin flow (TOFU) ----

func TestMatrixB_RemoteHTTPSNormalPinFlow(t *testing.T) {
	cert, der := selfSignedFor(t, "qbittorrent.home.arpa")
	fx := &qbFixture{torrents: 5, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewUnstartedServer(fx.handler())
	ts.TLS = tlsConfigFor(cert)
	ts.StartTLS()
	defer ts.Close()

	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})

	// Step 1: system-trust test against the self-signed endpoint fails
	// untrusted AND reports the offered fingerprint.
	res := h.mgr.Test(ctx(), TestParams{URL: ts.URL, TLSMode: TLSSystem})
	if res.OK || res.Status != StatusTLSUntrusted {
		t.Fatalf("test = %+v, want tls_untrusted", res)
	}
	want := qbittorrent.Fingerprint(der)
	if res.OfferedFingerprint != want {
		t.Fatalf("offered = %q, want %q", res.OfferedFingerprint, want)
	}

	// Step 2: configuring a pin that was NEVER captured by a test is
	// refused (pin_unknown) — a fingerprint alone must not be trustable
	// without the daemon having seen the certificate.
	fakePin := string(bytes.Repeat([]byte("0"), 64))
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSPin, Pin: fakePin}, SecretAction: "keep"})
	if cfg.OK || cfg.Rejection != RejectPinUnknown {
		t.Fatalf("configure = %+v, want pin_unknown", cfg)
	}

	// Step 3: pin-mode TEST with the CAPTURED fingerprint succeeds.
	res = h.mgr.Test(ctx(), TestParams{URL: ts.URL, TLSMode: TLSPin, Pin: want})
	if !res.OK || res.AppVersion != "v5.2.3" || res.WebAPIVersion != "2.15.1" {
		t.Fatalf("pinned test = %+v", res)
	}

	// Step 4: activate; the syncer connects over TLS with the pin.
	// (The fixture binds loopback, so the derived mode is "local" —
	// the remote/HTTPS aspects under test are transport and trust.)
	cfg = h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, TLSMode: TLSPin, Pin: want}, SecretAction: "keep"})
	if !cfg.OK || cfg.Transport != "https" {
		t.Fatalf("configure = %+v", cfg)
	}
	if ok := driveCycles(t, h.syncer, 2); !ok {
		t.Fatal("pinned cycles failed")
	}
	st := h.mgr.Status()
	if st.Status != StatusConnected || st.Transport != "https" || st.TLSMode != TLSPin {
		t.Fatalf("status = %+v", st)
	}
	if got := len(h.syncer.State().Torrents); got != 5 {
		t.Fatalf("torrents = %d", got)
	}

	// The persisted profile survives a "restart" (fresh Manager over
	// the same store — matrix O in the same breath).
	h2 := newHarnessOverStore(t, h.store, h.prov, h.syncer)
	st2 := h2.Status()
	if !st2.Configured || st2.TLSMode != TLSPin || st2.Host != stripScheme(ts.URL) {
		t.Fatalf("reloaded status = %+v", st2)
	}
}

// ---- C. hostname unreachable ----

func TestMatrixC_HostnameUnreachable(t *testing.T) {
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: "https://nonexistent.invalid:8443", TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	h.syncer.CycleForTest()
	st := h.mgr.Status()
	if st.Status != StatusUnreachable {
		t.Fatalf("status = %+v, want unreachable (DNS NXDOMAIN)", st)
	}
}

// ---- D. TCP refused ----

func TestMatrixD_TCPRefused(t *testing.T) {
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:1", "", "")
	})
	h.syncer.CycleForTest()
	if st := h.mgr.Status(); st.Status != StatusUnreachable {
		t.Fatalf("status = %+v, want unreachable (ECONNREFUSED)", st)
	}
}

// ---- E. auth required, none configured ----

func TestMatrixE_AuthRequired(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1", requireAuth: true}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	driveCycles(t, h.syncer, 1)
	if st := h.mgr.Status(); st.Status != StatusAuthRequired {
		t.Fatalf("status = %+v, want auth_required (403 without credentials)", st)
	}
}

// ---- F. bad password ----

func TestMatrixF_BadPassword(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1",
		username: "clement", password: "correct", requireAuth: true}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	h.prov.SetSecret([]byte("wrong"))

	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "clement", UseStoredPassword: true, TLSMode: TLSSystem},
		SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	// Syncer reaches the sticky auth_failed after 3 attempts; the
	// fixture counts sessions to prove login attempts were made.
	for i := 0; i < state.StickyAuthFails; i++ {
		h.syncer.CycleForTest()
	}
	st := h.mgr.Status()
	if st.Status != StatusAuthFailed {
		t.Fatalf("status = %+v, want auth_failed", st)
	}
	// Sticky: further cycles perform no new logins (frugality).
	before := fx.sessionCount()
	h.syncer.CycleForTest()
	if fx.sessionCount() != before {
		t.Fatal("sticky state still contacting the backend")
	}

	// A direct TEST with the bad stored password also reports
	// auth_failed — the settings surface shows exactly this.
	res := h.mgr.Test(ctx(), TestParams{URL: ts.URL, Username: "clement", UseStoredPassword: true, TLSMode: TLSSystem})
	if res.OK || res.Status != StatusAuthFailed {
		t.Fatalf("test = %+v", res)
	}
	// ...and with the CORRECT explicit password it succeeds (the fix
	// path the UI offers).
	res = h.mgr.Test(ctx(), TestParams{URL: ts.URL, Username: "clement", Password: []byte("correct"), TLSMode: TLSSystem})
	if !res.OK || res.Status != StatusConnected {
		t.Fatalf("test = %+v, want ok", res)
	}
}

// ---- G/H. expired session: one re-auth (success / failure) ----

func TestMatrixG_SessionExpiryReauthSuccess(t *testing.T) {
	fx := &qbFixture{torrents: 2, appVersion: "v5.2.3", apiVersion: "2.15.1",
		username: "clement", password: "hunter2", requireAuth: true}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	h.prov.SetSecret([]byte("hunter2"))
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "clement", UseStoredPassword: true, TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	if ok := driveCycles(t, h.syncer, 2); !ok {
		t.Fatal("initial cycles failed")
	}
	if st := h.mgr.Status(); st.Status != StatusConnected {
		t.Fatalf("pre-expiry status = %+v", st)
	}

	// Expire the session server-side: next maindata 403s once; the
	// adapter re-logins (bounded) and the FOLLOWING cycle recovers.
	fx.expireSessionOnce()
	h.syncer.CycleForTest() // hits 403 -> re-login -> success
	if st := h.mgr.Status(); st.Status != StatusConnected {
		t.Fatalf("post-expiry status = %+v, want connected after one reauth", st)
	}
}

func TestMatrixH_SessionExpiryReauthFailure(t *testing.T) {
	fx := &qbFixture{torrents: 2, appVersion: "v5.2.3", apiVersion: "2.15.1",
		username: "clement", password: "newpass", requireAuth: true}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	h.prov.SetSecret([]byte("oldpass")) // stale: server changed it (replace needs a password; keep binds the stored one)
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "clement", UseStoredPassword: true, TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	for i := 0; i < state.StickyAuthFails; i++ {
		h.syncer.CycleForTest()
	}
	if st := h.mgr.Status(); st.Status != StatusAuthFailed {
		t.Fatalf("status = %+v, want auth_failed after failed reauth", st)
	}
}

// ---- I/J. TLS failures ----

func TestMatrixI_InvalidCertificate(t *testing.T) {
	cert, _ := selfSignedFor(t, "qbittorrent.home.arpa")
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewUnstartedServer(fx.handler())
	ts.TLS = tlsConfigFor(cert)
	ts.StartTLS()
	defer ts.Close()

	res := directTest(t, ts.URL, TLSSystem)
	if res.Status != StatusTLSUntrusted || res.OfferedFingerprint == "" {
		t.Fatalf("test = %+v, want tls_untrusted with offered fingerprint", res)
	}
}

func TestMatrixJ_HostnameMismatch(t *testing.T) {
	// Pinned certificate valid ONLY for another host (no IP SAN): the
	// chain verifies via the pin anchor, the hostname does not —
	// tls_hostname, never a downgrade.
	cert, der := selfSignedHostOnly(t, "other.example")
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewUnstartedServer(fx.handler())
	ts.TLS = tlsConfigFor(cert)
	ts.StartTLS()
	defer ts.Close()

	res := directTest(t, ts.URL, TLSPin, qbittorrent.Fingerprint(der))
	if res.Status != StatusTLSHostname {
		t.Fatalf("test = %+v, want tls_hostname", res)
	}
}

// ---- K. remote HTTP: explicit warning policy ----

func TestMatrixK_RemoteHTTPPolicy(t *testing.T) {
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})
	// Non-loopback HTTP without acknowledgement: refused at TEST time.
	res := h.mgr.Test(ctx(), TestParams{URL: "http://192.0.2.10:8080", TLSMode: TLSSystem})
	if res.OK || res.Status != StatusInsecureHTTP {
		t.Fatalf("test = %+v, want insecure_http refusal", res)
	}
	// And at CONFIGURE time.
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: "http://192.0.2.10:8080", TLSMode: TLSSystem}, SecretAction: "keep"})
	if cfg.OK || cfg.Rejection != RejectInsecureHTTP {
		t.Fatalf("configure = %+v, want insecure_http rejection", cfg)
	}
	// With the explicit acknowledgement the profile activates and is
	// reported insecure (no network needed for this assertion — the
	// TEST-NET-2 host is unreachable; only the policy is proven here).
	cfg = h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: "http://192.0.2.10:8080", AllowInsecureHTTP: true, TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	if st := h.mgr.Status(); !st.Insecure || st.Transport != "http" {
		t.Fatalf("status = %+v, want insecure http flagged", st)
	}
	// Loopback HTTP never requires the acknowledgement.
	if res := h.mgr.Test(ctx(), TestParams{URL: "http://127.0.0.1:8080", TLSMode: TLSSystem}); res.Status == StatusInsecureHTTP {
		t.Fatal("loopback http flagged insecure")
	}
}

// ---- L. malformed base URL ----

func TestMatrixL_MalformedURL(t *testing.T) {
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})
	for _, bad := range []string{"ftp://x", "https://user:pw@h", "https://", "javascript:x", "https://h/#f"} {
		res := h.mgr.Test(ctx(), TestParams{URL: bad, TLSMode: TLSSystem})
		if res.OK || res.Status != StatusInvalidConfig {
			t.Errorf("test(%q) = %+v, want invalid_configuration", bad, res)
		}
		cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{URL: bad, TLSMode: TLSSystem}, SecretAction: "keep"})
		if cfg.OK || cfg.Rejection != RejectInvalidURL {
			t.Errorf("configure(%q) = %+v, want invalid_url", bad, cfg)
		}
	}
}

// ---- M. WebAPI version compatibility ----

// Design decision (docs/QBITTORRENT.md, adapter rules): there is NO
// hard version gate below the endpoints' own existence — a backend
// claiming an old WebAPI still syncs (stop/start vs pause/resume is
// version-gated per mutation, fixture-tested in internal/mutate).
// This pins that behavior: an old-version backend is NOT blocked.
func TestMatrixM_OldWebAPIStillSyncs(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v4.6.7", apiVersion: "2.9.3"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()
	res := directTest(t, ts.URL, TLSSystem)
	if !res.OK || res.WebAPIVersion != "2.9.3" {
		t.Fatalf("test = %+v, want ok with the reported (old) version", res)
	}
}

// ---- N. backend switch A -> B ----

func TestMatrixN_BackendSwitchAB(t *testing.T) {
	fxA := &qbFixture{torrents: 3, appVersion: "vA", apiVersion: "2.15.1"}
	tsA := httptest.NewServer(fxA.handler())
	defer tsA.Close()
	fxB := &qbFixture{torrents: 1, appVersion: "vB", apiVersion: "2.15.1"}
	tsB := httptest.NewServer(fxB.handler())
	defer tsB.Close()

	h := newHarness(t, localProfile(tsA.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(tsA.URL, "", "")
	})
	driveCycles(t, h.syncer, 1)
	if got := len(h.syncer.State().Torrents); got != 3 {
		t.Fatalf("A torrents = %d", got)
	}

	_, events, cancel := h.syncer.Subscribe()
	defer cancel()

	// A pending mutation BLOCKS the switch (mutation retargeting guard).
	hashA1 := fmt.Sprintf("%040x", 1)
	sub := h.mutator.Submit(mutate.Request{Action: mutate.Pause, Hash: hashA1, Ref: "r-a"})
	if sub.Outcome != mutate.OutcomeAccepted {
		t.Fatalf("submit = %+v", sub)
	}
	cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: tsB.URL, TLSMode: TLSSystem}, SecretAction: "keep"})
	if cfg.OK || cfg.Rejection != RejectMutationsPending {
		t.Fatalf("configure = %+v, want mutations_pending", cfg)
	}
	// Let it settle (timeout on A's state), then switch cleanly.
	time.Sleep(120 * time.Millisecond)
	h.mutator.ReconcileForTest()

	cfg = h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: tsB.URL, TLSMode: TLSSystem}, SecretAction: "keep"})
	if !cfg.OK || cfg.Epoch < 1 {
		t.Fatalf("configure = %+v", cfg)
	}

	// The A->B removal event was published; the fresh sync brought ONLY
	// B's torrents; versions updated; B's server saw rid=0 (fresh
	// session) as the first request.
	select {
	case ch := <-events:
		if len(ch.Removed) != 3 {
			t.Fatalf("switch removals = %d, want 3", len(ch.Removed))
		}
	default:
		t.Fatal("no removal event for backend A")
	}
	driveCycles(t, h.syncer, 2)
	stB := h.syncer.State()
	if len(stB.Torrents) != 1 || stB.AppVersion != "vB" {
		t.Fatalf("B state = %d torrents, version %q", len(stB.Torrents), stB.AppVersion)
	}
	if first := fxB.firstRID(); first != 0 {
		t.Fatalf("B's first rid = %d, want 0 (fresh session/rid)", first)
	}
	if st := h.mgr.Status(); st.Status != StatusConnected || st.Host != stripScheme(tsB.URL) {
		t.Fatalf("status = %+v", st)
	}

	// Reconnect after B "fails" stays on B (recovery never falls back
	// to A): stop B, cycle (degraded), restart B, cycle (recovered).
	tsB.Close()
	h.syncer.CycleForTest()
	if st := h.mgr.Status(); st.Status == StatusConnected {
		t.Fatal("B down but status connected")
	}
	tsB2 := httptest.NewServer(fxB.handler())
	defer tsB2.Close()
	// B's URL changed port — the point is the daemon keeps using the
	// CONFIGURED backend (which is now dead); it does not revert to A.
	if st := h.mgr.Status(); st.Host == stripScheme(tsA.URL) {
		t.Fatal("daemon fell back to backend A after B failure")
	}
}

// ---- O. daemon restart with a remote profile ----

func TestMatrixO_RestartWithRemoteProfile(t *testing.T) {
	fx := &qbFixture{torrents: 4, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	store := profilePath(t)
	prov := &secrets.Fake{}
	prov.SetSecret([]byte("pw"))
	fallback := DefaultProfile()

	// First incarnation: local profile, configure the remote.
	syncer1 := state.New(mustClient(t, fallback, prov), state.Options{}, nil)
	mut1 := mutate.New(mustClient(t, fallback, prov), syncer1, mutate.Options{}, nil)
	mgr1, err := NewManager(store, fallback, prov, syncer1, syncer1, mut1, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if cfg := mgr1.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", UseStoredPassword: true, TLSMode: TLSSystem}, SecretAction: "keep"}); !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}

	// Second incarnation ("restart"): profile + secret reloaded from
	// their stores, fresh epoch, sync succeeds.
	syncer2 := state.New(mustClient(t, mgr1.Profile(), prov), state.Options{}, nil)
	mut2 := mutate.New(mustClient(t, mgr1.Profile(), prov), syncer2, mutate.Options{}, nil)
	mgr2, err := NewManager(store, fallback, prov, syncer2, syncer2, mut2, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	driveCycles(t, syncer2, 2)
	st := mgr2.Status()
	if !st.Configured || st.Status != StatusConnected || !st.HasSecret {
		t.Fatalf("restarted status = %+v", st)
	}
	if got := len(syncer2.State().Torrents); got != 4 {
		t.Fatalf("torrents after restart = %d", got)
	}
}

// ---- configure secret intents ----

func TestConfigureSecretIntents(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()
	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})

	// replace stores the explicit password.
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", Password: []byte("first"), TLSMode: TLSSystem}, SecretAction: "replace"}); !cfg.OK {
		t.Fatalf("replace = %+v", cfg)
	}
	if string(h.prov.Secret) != "first" {
		t.Fatalf("provider secret = %q", h.prov.Secret)
	}
	// keep preserves it across a form open/close (no accidental erase).
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", TLSMode: TLSSystem}, SecretAction: "keep"}); !cfg.OK {
		t.Fatalf("keep = %+v", cfg)
	}
	if string(h.prov.Secret) != "first" {
		t.Fatal("keep erased the stored secret")
	}
	// delete removes it.
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", TLSMode: TLSSystem}, SecretAction: "delete"}); !cfg.OK {
		t.Fatalf("delete = %+v", cfg)
	}
	if h.prov.Secret != nil {
		t.Fatal("delete did not remove the secret")
	}

	// An unavailable provider surfaces secrets_unavailable on replace.
	h.prov.SetUnavailable(true)
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", Password: []byte("x"), TLSMode: TLSSystem}, SecretAction: "replace"}); cfg.OK || cfg.Rejection != RejectSecretsMissing {
		t.Fatalf("replace = %+v, want secrets_unavailable", cfg)
	}
}

// Status derivation while the profile requires a secret but the store
// is unusable -> secrets_unavailable (distinct from auth_failed).
func TestStatusSecretsUnavailable(t *testing.T) {
	fx := &qbFixture{torrents: 1, appVersion: "v5.2.3", apiVersion: "2.15.1",
		username: "u", password: "pw", requireAuth: true}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()
	h := newHarness(t, localProfile(ts.URL), func() (*qbittorrent.Client, error) {
		return qbittorrent.New(ts.URL, "", "")
	})
	if cfg := h.mgr.Configure(ctx(), ConfigureParams{TestParams: TestParams{
		URL: ts.URL, Username: "u", Password: []byte("pw"), TLSMode: TLSSystem}, SecretAction: "replace"}); !cfg.OK {
		t.Fatalf("configure = %+v", cfg)
	}
	// Break the store AFTER configuration: the next login attempts fail
	// with credentials-unavailable (not bad credentials).
	h.prov.SetUnavailable(true)
	h.syncer.CycleForTest()
	if st := h.mgr.Status(); st.Status != StatusSecretsUnavailable {
		t.Fatalf("status = %+v, want secrets_unavailable", st)
	}
}

// ---- fixture/test helpers ----

func (f *qbFixture) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions
}

func (f *qbFixture) expireSessionOnce() {
	f.mu.Lock()
	f.maindataErr = 1 // one 403 (session unknown), then normal
	f.mu.Unlock()
}

func (f *qbFixture) firstRID() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.maindataRIDs) == 0 {
		return -1
	}
	return f.maindataRIDs[0]
}

// driveCycles runs n cycles and returns the last one's ok. Degraded
// backends legitimately fail cycles — the STATUS assertions decide.
func driveCycles(t *testing.T, s *state.Syncer, n int) bool {
	t.Helper()
	ok := false
	for i := 0; i < n; i++ {
		done := make(chan bool, 1)
		go func() { done <- s.CycleForTest() }()
		select {
		case ok = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("cycle timed out")
		}
	}
	return ok
}

func mustClient(t *testing.T, p Profile, prov secrets.Provider) *qbittorrent.Client {
	t.Helper()
	c, err := BuildClient(p, prov)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newHarnessOverStore(t *testing.T, store string, prov secrets.Provider, syncer *state.Syncer) *Manager {
	t.Helper()
	mut := mutate.New(mustClient(t, DefaultProfile(), prov), syncer, mutate.Options{}, quietLog())
	mgr, err := NewManager(store, DefaultProfile(), prov, syncer, syncer, mut, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func stripScheme(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		return u[i+3:]
	}
	return u
}

// directTest runs a one-shot Manager test against url with the given
// TLS mode (and optional pin), no daemon state required.
func directTest(t *testing.T, url, tlsMode string, pins ...string) TestResult {
	t.Helper()
	h := newHarness(t, DefaultProfile(), func() (*qbittorrent.Client, error) {
		return qbittorrent.New("http://127.0.0.1:8080", "", "")
	})
	p := TestParams{URL: url, TLSMode: tlsMode}
	if len(pins) > 0 {
		p.Pin = pins[0]
	}
	// Seed the offered-cert cache for pin tests that never ran a prior
	// system-trust probe against this server.
	if tlsMode == TLSPin && p.Pin != "" {
		h.mgr.Test(ctx(), TestParams{URL: url, TLSMode: TLSSystem})
	}
	return h.mgr.Test(ctx(), p)
}

// ---- TLS fixture helpers (pure stdlib, mirrors the adapter tests) ----

func selfSignedFor(t *testing.T, host string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{host},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, der
}

func tlsConfigFor(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}}
}

func selfSignedHostOnly(t *testing.T, host string) (tls.Certificate, []byte) {
	// A certificate whose only reference is the DNS name (no IP SAN).
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der2, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pair2, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der2}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return pair2, der2
}

// §29 benchmark: the connection.test path (validation + login-less
// probe + two version GETs) against a local fixture.
func BenchmarkConnectionTestPath(b *testing.B) {
	fx := &qbFixture{torrents: 0, appVersion: "v5.2.3", apiVersion: "2.15.1"}
	ts := httptest.NewServer(fx.handler())
	defer ts.Close()

	prov := &secrets.Fake{}
	syncer := state.New(mustClientB(b, DefaultProfile(), prov), state.Options{}, nil)
	mut := mutate.New(mustClientB(b, DefaultProfile(), prov), syncer, mutate.Options{}, nil)
	mgr, err := NewManager(profilePathB(b), DefaultProfile(), prov, syncer, syncer, mut, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := mgr.Test(context.Background(), TestParams{URL: ts.URL, TLSMode: TLSSystem})
		if !res.OK {
			b.Fatalf("test = %+v", res)
		}
	}
}

func mustClientB(b *testing.B, p Profile, prov secrets.Provider) *qbittorrent.Client {
	c, err := BuildClient(p, prov)
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func profilePathB(b *testing.B) string {
	return filepath.Join(b.TempDir(), "omatorrent", "connection.json")
}
