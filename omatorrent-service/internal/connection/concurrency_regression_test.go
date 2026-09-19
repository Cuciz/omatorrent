package connection

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/mutate"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/secrets"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// ============================================================
// Deterministic stale-snapshot concurrency regression (final
// external-review blocker).
//
// Mechanism: a gating provider whose armed Get call SIGNALS entry and
// blocks on a channel — pausing T2 exactly inside its secret-snapshot
// phase: on the OLD (buggy) ordering that is BEFORE T2 reaches
// BeginSwitch; on the fixed ordering it is AFTER T2 acquired the
// drain (T2 owns the switch slot while paused). No sleeps, no
// scheduler luck: every step is channel-synchronized.
//
// Historical bug (old ordering snapshot→BeginSwitch):
//
//	T2 snapshots A → pauses → T1 commits B → T2 resumes, fails
//	after mutating → rollback restores A over committed B.
//
// Distinguishing invariant: IF T1 reported success, then after T2's
// failure the persisted profile MUST still be T1's B — a committed
// activation can never be rolled back by another transaction. On the
// fixed ordering T1 is refused (mutations_pending) and
// observationally inert, and the final state is A everywhere (T2 was
// the only mutating transaction and it failed).
// ============================================================

// gatingProvider delegates to a Fake; the ARMED Get call (by call
// number) signals `entered` and blocks until `release` closes. Every
// Get also signals `sawGet` (non-blocking) so tests can wait for the
// Manager's background presence probe deterministically.
type gatingProvider struct {
	secrets.Provider
	mu      sync.Mutex
	gets    int
	armed   int  // 0 = disarmed; else the call number to block
	blocked bool // one-shot

	entered chan struct{}
	release chan struct{}
	sawGet  chan struct{}
}

func newGatingProvider(f *secrets.Fake) *gatingProvider {
	return &gatingProvider{Provider: f, sawGet: make(chan struct{}, 64)}
}

func (g *gatingProvider) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gets
}

func (g *gatingProvider) arm(n int) (entered chan struct{}, release chan struct{}) {
	g.mu.Lock()
	g.armed = n
	g.mu.Unlock()
	g.entered = make(chan struct{})
	g.release = make(chan struct{})
	return g.entered, g.release
}

func (g *gatingProvider) Get(ctx context.Context) ([]byte, bool, error) {
	g.mu.Lock()
	g.gets++
	n := g.gets
	armed := g.armed
	blocked := g.blocked
	g.mu.Unlock()
	select {
	case g.sawGet <- struct{}{}:
	default:
	}
	if armed == n && !blocked {
		g.mu.Lock()
		g.blocked = true
		g.mu.Unlock()
		close(g.entered)
		<-g.release
	}
	return g.Provider.Get(ctx)
}

// detHarness wires a Manager like newHarness but around a gating
// provider.
type detHarness struct {
	t       *testing.T
	prov    *secrets.Fake
	gate    *gatingProvider
	syncer  *state.Syncer
	mutator *mutate.Mutator
	mgr     *Manager
	store   string
}

func newDetHarness(t *testing.T, url string) *detHarness {
	t.Helper()
	prov := &secrets.Fake{}
	gate := newGatingProvider(prov)
	client, err := qbittorrent.New(url, "", "")
	if err != nil {
		t.Fatal(err)
	}
	syncer := state.New(client, state.Options{}, quietLog())
	mut := mutate.New(client, syncer, mutate.Options{ReconcileWindow: 60 * time.Millisecond}, quietLog())
	store := profilePath(t)
	mgr, err := NewManager(store, localProfile(url), gate, syncer, syncer, mut, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	mgr.testPace = 0
	return &detHarness{t: t, prov: prov, gate: gate, syncer: syncer, mutator: mut, mgr: mgr, store: store}
}

func TestDeterministicStaleSnapshotConcurrency(t *testing.T) {
	newServer := func(v string) *httptest.Server {
		fx := &qbFixture{torrents: 1, appVersion: v, apiVersion: "2.15.1"}
		ts := httptest.NewServer(fx.handler())
		t.Cleanup(ts.Close)
		return ts
	}
	tsA, tsB, tsC := newServer("vA"), newServer("vB"), newServer("vC")

	h := newDetHarness(t, tsA.URL)

	// Wait for the background presence probe's Get (call #1) — pure
	// channel sync, then persist A with secret secA.
	<-h.gate.sawGet
	if cfg := h.mgr.Configure(context.Background(), ConfigureParams{TestParams: TestParams{
		URL: tsA.URL, Username: "a", Password: []byte("secA"), TLSMode: TLSSystem}, SecretAction: "replace"}); !cfg.OK {
		t.Fatalf("persist A = %+v", cfg)
	}
	// Settle the counts AFTER the initial transaction (its Gets are
	// done) so T1's inertness can be measured against this baseline.
	baseGets := h.gate.count()
	baseStores := h.prov.StoreCalls

	// Arm the NEXT Get (call baseGets+1) to block: that will be T2's
	// secret snapshot.
	entered, release := h.gate.arm(baseGets + 1)

	// T2: A → C with a replace; pauses inside its secret snapshot.
	t2done := make(chan ConfigureResult, 1)
	go func() {
		t2done <- h.mgr.Configure(context.Background(), ConfigureParams{TestParams: TestParams{
			URL: tsC.URL, Username: "c", Password: []byte("secC"), TLSMode: TLSSystem}, SecretAction: "replace"})
	}()
	<-entered // T2 is now paused at its snapshot, pre-BeginSwitch (old) or drain-holding (new).

	// T1: A → B while T2 is paused.
	baseBytes, _, _ := ReadStoreRaw(h.store)
	baseEpoch := h.mgr.Status().Epoch
	t1 := h.mgr.Configure(context.Background(), ConfigureParams{TestParams: TestParams{
		URL: tsB.URL, Username: "b", Password: []byte("secB"), TLSMode: TLSSystem}, SecretAction: "replace"})

	if t1.OK {
		// OLD ordering: T1 committed B while T2 held a stale A
		// snapshot. Make T2 fail AFTER its persistence point (secret
		// Store + SaveStore both succeed; the build-failure hook
		// aborts next) so T2's rollback actually EXECUTES, and prove
		// the committed B survives — on the buggy ordering T2's
		// rollback resurrects A and this FAILS.
		buildFailureHook = func(next Profile) error { return errInjected }
		defer func() { buildFailureHook = nil }()
		close(release)
		r2 := <-t2done
		if r2.OK {
			t.Fatal("T2 unexpectedly succeeded")
		}
		persisted, _, err := LoadStore(h.store)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.URL != tsB.URL || persisted.Username != "b" {
			t.Fatalf("STALE ROLLBACK: T1 committed B, but after T2's failure the persisted profile is %+v — a committed activation was rolled back", persisted.URL)
		}
		if cur := h.mgr.Profile(); cur.URL != tsB.URL {
			t.Fatalf("runtime profile = %s after committed B + failed T2", cur.URL)
		}
		if string(h.prov.SecretValue()) != "secB" {
			t.Fatalf("secret = %q, want T1's secB (committed)", h.prov.SecretValue())
		}
		return
	}

	// FIXED ordering: T1 must be the inert refusal.
	if t1.Rejection != RejectMutationsPending {
		t.Fatalf("T1 = %+v, want mutations_pending while T2 owns the drain", t1)
	}
	// Observational inertness of the losing transaction. T2's own
	// armed snapshot Get already counted (+1) and it has NOT reached
	// its Store yet (paused), so ANY further delta is T1's.
	afterBytes, _, _ := ReadStoreRaw(h.store)
	if string(baseBytes) != string(afterBytes) {
		t.Fatal("losing configure mutated the store")
	}
	if h.gate.count() != baseGets+1 {
		t.Fatalf("losing configure called the provider: gets %d -> %d", baseGets, h.gate.count())
	}
	if h.prov.StoreCalls != baseStores || h.prov.DelCalls != 0 {
		t.Fatal("losing configure mutated the secret provider")
	}
	if e := h.mgr.Status().Epoch; e != baseEpoch {
		t.Fatalf("losing configure changed the epoch: %d -> %d", baseEpoch, e)
	}

	// Now T2 fails after a mutation point: Store(secC) and SaveStore(C)
	// both succeed, then the build-failure hook aborts → rollback to
	// T2's OWN snapshot (A) — with the rollback writes really landing.
	buildFailureHook = func(next Profile) error { return errInjected }
	defer func() { buildFailureHook = nil }()
	close(release)
	r2 := <-t2done
	if r2.OK || r2.Rejection != RejectInvalidURL {
		t.Fatalf("T2 = %+v, want invalid_url (injected post-persist failure)", r2)
	}

	// Final state: A everywhere; B was never committed; nothing stale.
	if cur := h.mgr.Profile(); cur.URL != tsA.URL || cur.Username != "a" {
		t.Fatalf("runtime profile = %+v, want A", cur)
	}
	persisted, exists, err := LoadStore(h.store)
	if err != nil || !exists {
		t.Fatalf("persisted unreadable: exists=%v err=%v", exists, err)
	}
	if persisted.URL != tsA.URL || persisted.Username != "a" {
		t.Fatalf("persisted profile = %+v, want A", persisted)
	}
	if string(h.prov.SecretValue()) != "secA" {
		t.Fatalf("secret = %q, want restored secA", h.prov.SecretValue())
	}
	// Restart loads A.
	syncer2 := state.New(mustClient(t, persisted, h.prov), state.Options{}, quietLog())
	mgr2, err := NewManager(h.store, DefaultProfile(), h.prov, syncer2, syncer2, h.mutator, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if p := mgr2.Profile(); p.URL != tsA.URL || p.Username != "a" {
		t.Fatalf("post-restart profile = %+v, want A", p)
	}
}

// errInjected is the deterministic post-persistence failure used by
// the concurrency regression.
var errInjected = errors.New("injected post-persistence failure")
