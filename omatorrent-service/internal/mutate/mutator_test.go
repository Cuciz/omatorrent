package mutate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// ---- fakes ----

type fakeBackend struct {
	mu      sync.Mutex
	calls   []string // one entry per mutation call
	err     error    // when non-nil, every call fails with this
	echo    string   // AddMagnet echo hash (modern response)
	block   chan struct{}
	blocked int
}

func (f *fakeBackend) rec(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if f.block != nil {
		f.mu.Lock()
		f.blocked++
		f.mu.Unlock()
		<-f.block
	}
}

func (f *fakeBackend) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeBackend) failWith(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

func (f *fakeBackend) call(name string) error {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	f.rec(name)
	return nil
}

func (f *fakeBackend) StopTorrent(ctx context.Context, hash string) error {
	return f.call("stop " + hash)
}
func (f *fakeBackend) StartTorrent(ctx context.Context, hash string) error {
	return f.call("start " + hash)
}
func (f *fakeBackend) PauseTorrent(ctx context.Context, hash string) error {
	return f.call("pause " + hash)
}
func (f *fakeBackend) ResumeTorrent(ctx context.Context, hash string) error {
	return f.call("resume " + hash)
}
func (f *fakeBackend) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	name := "delete " + hash + " files=" + strings.ToLower(map[bool]string{true: "true", false: "false"}[deleteFiles])
	return f.call(name)
}
func (f *fakeBackend) AddMagnet(ctx context.Context, magnet string) ([]string, error) {
	f.mu.Lock()
	err, echo := f.err, f.echo
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f.rec("add " + magnet)
	if echo != "" {
		return []string{echo}, nil
	}
	return nil, nil
}

// fakeState is a controllable StateSource.
type fakeState struct {
	mu   sync.Mutex
	st   state.State
	ch   chan state.Change
	subs int
}

func newFakeState(torrents map[string]state.Torrent) *fakeState {
	if torrents == nil {
		torrents = map[string]state.Torrent{}
	}
	return &fakeState{
		st: state.State{BackendOK: true, WebAPIVersion: "2.15.1", Torrents: torrents},
		ch: make(chan state.Change, 16),
	}
}

func (f *fakeState) State() state.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.st
	st.Torrents = map[string]state.Torrent{}
	for k, v := range f.st.Torrents {
		st.Torrents[k] = v
	}
	return st
}

func (f *fakeState) Health() bool { return f.State().BackendOK }

func (f *fakeState) Subscribe() (state.State, <-chan state.Change, func()) {
	return f.State(), f.ch, func() {}
}

// set replaces the torrent map and notifies the reconcile loop.
func (f *fakeState) set(torrents map[string]state.Torrent) {
	f.mu.Lock()
	f.st.Torrents = torrents
	f.mu.Unlock()
	f.ch <- state.Change{}
}

func (f *fakeState) setDegraded() {
	f.mu.Lock()
	f.st.BackendOK = false
	f.mu.Unlock()
	f.ch <- state.Change{}
}

const h1 = "0123456789abcdef0123456789abcdef01234567"

func torrent(hash, st string) state.Torrent { return state.Torrent{Hash: hash, State: st} }

// newTestMutator wires a mutator with fast windows and a running
// reconcile loop.
func newTestMutator(b *fakeBackend, s *fakeState) (*Mutator, func()) {
	m := New(b, s, Options{ReconcileWindow: 400 * time.Millisecond, SubmitTimeout: 500 * time.Millisecond, MaxInFlight: 4, RingSize: 8}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(context.Background())
	}()
	return m, func() {}
}

func waitResult(t *testing.T, m *Mutator) Result {
	t.Helper()
	ch, cancel := m.Results()
	defer cancel()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no result within 3s")
		return Result{}
	}
}

// ---- validation (stage 1, before any backend call) ----

func TestSubmitStaleTorrent(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(nil) // empty state: nothing exists
	m := New(b, s, Options{}, nil)
	s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	if s1.Outcome != CodeStaleTorrent {
		t.Fatalf("outcome = %q, want stale_torrent", s1.Outcome)
	}
	if len(b.recorded()) != 0 {
		t.Fatalf("backend called: %v", b.recorded())
	}
	// A rejected ref replays as the same rejection, never executes later.
	s1 = m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	if s1.Outcome != CodeStaleTorrent {
		t.Fatalf("replay outcome = %q", s1.Outcome)
	}
	if len(b.recorded()) != 0 {
		t.Fatalf("backend called after replay: %v", b.recorded())
	}
}

func TestSubmitInvalidMagnet(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(nil)
	m := New(b, s, Options{}, nil)
	for _, bad := range []string{
		"http://example.com/x.torrent",
		"magnet:?dn=nohash",
		"magnet:?xt=urn:btih:NOTHEX",
		"magnet:?xt=urn:sha1:abcdef0123456789abcdef0123456789abcdef01",
	} {
		if s1 := m.Submit(Request{Action: Add, URL: bad, Ref: "r" + bad}); s1.Outcome != CodeInvalidURL {
			t.Fatalf("url %q outcome = %q, want invalid_url", bad, s1.Outcome)
		}
	}
	if len(b.recorded()) != 0 {
		t.Fatalf("backend called: %v", b.recorded())
	}
}

func TestSubmitDuplicateMagnet(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m := New(b, s, Options{}, nil)
	s1 := m.Submit(Request{Action: Add, URL: "magnet:?xt=urn:btih:" + h1, Ref: "r1"})
	if s1.Outcome != CodeDuplicate {
		t.Fatalf("outcome = %q, want duplicate", s1.Outcome)
	}
	if len(b.recorded()) != 0 {
		t.Fatal("backend called for duplicate")
	}
}

func TestSubmitBackendUnavailable(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	s.setDegraded()
	m := New(b, s, Options{}, nil)
	if s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"}); s1.Outcome != CodeBackendUnavailable {
		t.Fatalf("outcome = %q, want backend_unavailable", s1.Outcome)
	}
	if len(b.recorded()) != 0 {
		t.Fatal("backend called while degraded")
	}
}

func TestParseMagnetHash(t *testing.T) {
	if h, ok := ParseMagnetHash("magnet:?xt=urn:btih:" + strings.ToUpper(h1) + "&dn=x"); !ok || h != h1 {
		t.Fatalf("uppercase parse = %q %v (want lowercased)", h, ok)
	}
	if h, ok := ParseMagnetHash("magnet:?dn=x&xt=urn:btih:" + h1); !ok || h != h1 {
		t.Fatalf("second-param parse = %q %v", h, ok)
	}
	// v2 (64 hex) accepted.
	v2 := strings.Repeat("ab", 32)
	if h, ok := ParseMagnetHash("magnet:?xt=urn:btih:" + v2); !ok || h != v2 {
		t.Fatalf("v2 parse = %q %v", h, ok)
	}
}

// ---- version gating ----

func TestVersionGating(t *testing.T) {
	cases := []struct {
		webapi string
		want   string // prefix of the recorded backend call
	}{
		{"2.15.1", "stop"}, {"2.11.0", "stop"}, {"", "stop"},
		{"2.10.0", "pause"}, {"2.9.2", "pause"}, {"garbage", "pause"},
	}
	for _, tc := range cases {
		b := &fakeBackend{}
		s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
		s.mu.Lock()
		s.st.WebAPIVersion = tc.webapi
		s.mu.Unlock()
		m := New(b, s, Options{}, nil)
		s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
		if s1.Outcome != OutcomeAccepted {
			t.Fatalf("webapi=%q outcome=%q", tc.webapi, s1.Outcome)
		}
		got := b.recorded()
		if len(got) != 1 || !strings.HasPrefix(got[0], tc.want) {
			t.Fatalf("webapi=%q calls=%v, want prefix %q", tc.webapi, got, tc.want)
		}
	}
}

func TestResumeVersionGating(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StatePaused)})
	s.mu.Lock()
	s.st.WebAPIVersion = "2.9.2"
	s.mu.Unlock()
	m := New(b, s, Options{}, nil)
	if s1 := m.Submit(Request{Action: Resume, Hash: h1, Ref: "r1"}); s1.Outcome != OutcomeAccepted {
		t.Fatalf("outcome=%q", s1.Outcome)
	}
	if got := b.recorded(); len(got) != 1 || !strings.HasPrefix(got[0], "resume") {
		t.Fatalf("calls=%v, want legacy resume", got)
	}
}

// ---- reconciliation (stage 2) ----

func TestPauseReconciles(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{}) // unblock Run

	s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	if s1.Outcome != OutcomeAccepted || s1.Mutation != 1 || s1.Hash != h1 {
		t.Fatalf("stage1 = %+v", s1)
	}
	if got := b.recorded(); len(got) != 1 || got[0] != "stop "+h1 {
		t.Fatalf("backend calls = %v", got)
	}

	// State flips to paused → confirmed.
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StatePaused)})
	r := waitResult(t, m)
	if r.Status != StatusConfirmed || r.Action != Pause || r.Hash != h1 || r.Mutation != 1 {
		t.Fatalf("result = %+v", r)
	}
}

func TestResumeReconciles(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StatePaused)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	if s1 := m.Submit(Request{Action: Resume, Hash: h1, Ref: "r1"}); s1.Outcome != OutcomeAccepted {
		t.Fatalf("stage1 = %+v", s1)
	}
	// Any non-paused observed state confirms.
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StateQueued)})
	if r := waitResult(t, m); r.Status != StatusConfirmed {
		t.Fatalf("result = %+v", r)
	}
}

func TestAddReconciles(t *testing.T) {
	b := &fakeBackend{echo: h1}
	s := newFakeState(nil)
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	s1 := m.Submit(Request{Action: Add, URL: "magnet:?xt=urn:btih:" + h1 + "&dn= disposable", Ref: "r1"})
	if s1.Outcome != OutcomeAccepted || s1.Hash != h1 {
		t.Fatalf("stage1 = %+v (hash must be the parsed infohash)", s1)
	}
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	if r := waitResult(t, m); r.Status != StatusConfirmed || r.Hash != h1 {
		t.Fatalf("result = %+v", r)
	}
}

func TestRemoveReconciles(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateSeeding)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	s1 := m.Submit(Request{Action: Remove, Hash: h1, DeleteFiles: false, Ref: "r1"})
	if s1.Outcome != OutcomeAccepted {
		t.Fatalf("stage1 = %+v", s1)
	}
	if got := b.recorded(); got[0] != "delete "+h1+" files=false" {
		t.Fatalf("delete call = %v (deleteFiles must be explicit)", got)
	}
	s.set(map[string]state.Torrent{}) // disappeared
	if r := waitResult(t, m); r.Status != StatusConfirmed {
		t.Fatalf("result = %+v", r)
	}
}

func TestRemoveWithFilesForwardsExplicitIntent(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateSeeding)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Remove, Hash: h1, DeleteFiles: true, Ref: "r1"})
	if got := b.recorded(); got[0] != "delete "+h1+" files=true" {
		t.Fatalf("delete call = %v", got)
	}
	s.set(map[string]state.Torrent{})
	waitResult(t, m)
}

func TestTimeoutWhenStateNeverConfirms(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	// State never flips; the 400 ms window must expire with a truthful
	// ambiguous timeout (not a fake success or failure).
	r := waitResult(t, m)
	if r.Status != StatusTimeout {
		t.Fatalf("result = %+v, want timeout", r)
	}
}

func TestDegradedBackendNeverConfirms(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	// The state flips to paused but is simultaneously degraded: a
	// degraded snapshot is not confirming evidence.
	s.mu.Lock()
	s.st.Torrents = map[string]state.Torrent{h1: torrent(h1, state.StatePaused)}
	s.st.BackendOK = false
	s.mu.Unlock()
	s.ch <- state.Change{}
	r := waitResult(t, m)
	if r.Status != StatusTimeout {
		t.Fatalf("result = %+v, want timeout (degraded state cannot confirm)", r)
	}
}

// ---- replay / dedup ----

func TestReplayAfterCompletionDoesNotReexecute(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StatePaused)})
	waitResult(t, m)

	// Replay the same ref: the recorded terminal result answers; the
	// backend must NOT be called again.
	s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	if s1.Replay == nil || s1.Replay.Status != StatusConfirmed || s1.Replay.Mutation != 1 {
		t.Fatalf("replay = %+v, want recorded confirmed result", s1)
	}
	if got := b.recorded(); len(got) != 1 {
		t.Fatalf("backend calls = %v (destructive replay protection)", got)
	}
}

func TestReplayRemoveNeverExecutesTwice(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateSeeding)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Remove, Hash: h1, DeleteFiles: true, Ref: "rr"})
	s.set(map[string]state.Torrent{})
	waitResult(t, m)

	// After removal the hash is gone from state: a naive resubmission
	// would be stale_torrent, but the REPLAY must return the recorded
	// result instead of even looking at state.
	s1 := m.Submit(Request{Action: Remove, Hash: h1, DeleteFiles: true, Ref: "rr"})
	if s1.Replay == nil || s1.Replay.Status != StatusConfirmed {
		t.Fatalf("replay = %+v", s1)
	}
	if got := b.recorded(); len(got) != 1 {
		t.Fatalf("backend calls = %v — remove executed twice!", got)
	}
}

func TestReplayInFlightReturnsSameMutation(t *testing.T) {
	b := &fakeBackend{}
	unblock := make(chan struct{})
	b.mu.Lock()
	b.block = unblock
	b.mu.Unlock()
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m := New(b, s, Options{ReconcileWindow: time.Second, SubmitTimeout: 2 * time.Second}, nil)

	done := make(chan Stage1, 1)
	go func() { done <- m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"}) }()
	time.Sleep(50 * time.Millisecond) // first submission now blocked in backend
	s1b := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	if s1b.Outcome != OutcomeAccepted {
		t.Fatalf("in-flight replay = %+v", s1b)
	}
	close(unblock)
	first := <-done
	if first.Outcome != OutcomeAccepted {
		t.Fatalf("first = %+v", first)
	}
	if first.Mutation != s1b.Mutation {
		t.Fatalf("mutation ids differ: %d vs %d", first.Mutation, s1b.Mutation)
	}
}

func TestRefConflict(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"})
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StatePaused)})
	waitResult(t, m)

	// Same ref, different action: refused.
	if s1 := m.Submit(Request{Action: Remove, Hash: h1, DeleteFiles: false, Ref: "r1"}); s1.Outcome != CodeRefConflict {
		t.Fatalf("outcome = %q, want ref_conflict", s1.Outcome)
	}
	// Same ref, different delete_files intent: refused.
	if s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r2"}); s1.Outcome != OutcomeAccepted {
		t.Fatalf("different ref fine: %+v", s1)
	}
}

// ---- concurrency cap ----

func TestBusyCap(t *testing.T) {
	b := &fakeBackend{}
	unblock := make(chan struct{})
	b.mu.Lock()
	b.block = unblock
	b.mu.Unlock()
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m := New(b, s, Options{ReconcileWindow: time.Second, SubmitTimeout: 2 * time.Second, MaxInFlight: 1}, nil)

	go func() { m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"}) }()
	time.Sleep(50 * time.Millisecond)
	if s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r2"}); s1.Outcome != CodeBusy {
		t.Fatalf("outcome = %q, want busy", s1.Outcome)
	}
	close(unblock)
}

// ---- submission failure classification ----

func TestSubmitFailureClassification(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{qbittorrent.ErrUnreachable, CodeBackendUnavailable},
		{qbittorrent.ErrUnauthorized, CodeBackendUnavailable},
		{qbittorrent.ErrConflict, CodeBackendRejected},
		{errors.New("boom"), CodeBackendRejected},
	}
	for _, tc := range cases {
		b := &fakeBackend{}
		s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
		m := New(b, s, Options{}, nil)
		b.failWith(tc.err)
		if s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"}); s1.Outcome != tc.want {
			t.Fatalf("err=%v outcome=%q want %q", tc.err, s1.Outcome, tc.want)
		}
		// The failed ref replays as its rejection.
		if s1 := m.Submit(Request{Action: Pause, Hash: h1, Ref: "r1"}); s1.Outcome != tc.want {
			t.Fatalf("replay outcome=%q want %q", s1.Outcome, tc.want)
		}
	}
}

// The backend's echo (≥ 5.2.0) must match the daemon-parsed infohash —
// a foreign identity is a rejected submission, never a silent swap.
func TestAddEchoMismatchRejected(t *testing.T) {
	other := strings.Repeat("11", 20)
	b := &fakeBackend{echo: other}
	s := newFakeState(nil)
	m := New(b, s, Options{}, nil)
	if s1 := m.Submit(Request{Action: Add, URL: "magnet:?xt=urn:btih:" + h1, Ref: "r1"}); s1.Outcome != CodeBackendRejected {
		t.Fatalf("outcome = %q, want backend_rejected", s1.Outcome)
	}
}

// ---- ring bounds ----

func TestRingBounded(t *testing.T) {
	b := &fakeBackend{}
	s := newFakeState(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	m := New(b, s, Options{RingSize: 4}, nil)
	// Stage-1 rejections are recorded (a replayed ref must never
	// execute later what was refused); ten distinct stale refs fill and
	// cap the ring at 4.
	ghosts := []string{
		"ffffffffffffffffffffffffffffffffffffffff",
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}
	for i := 0; i < 10; i++ {
		if s1 := m.Submit(Request{Action: Pause, Hash: ghosts[i%2], Ref: "r" + string(rune('a'+i))}); s1.Outcome != CodeStaleTorrent {
			t.Fatalf("i=%d outcome=%q", i, s1.Outcome)
		}
	}
	m.mu.Lock()
	n := len(m.ring)
	m.mu.Unlock()
	if n != 4 {
		t.Fatalf("ring size = %d, want 4", n)
	}
}

// Regression (caught live by the Phase 0.3 smoke): an add replay must
// match on the magnet URL — add requests carry no hash on the wire, so
// hash comparison wrongly returned ref_conflict.
func TestReplayAddMatchesOnURL(t *testing.T) {
	b := &fakeBackend{echo: h1}
	s := newFakeState(nil)
	m, _ := newTestMutator(b, s)
	defer s.set(map[string]state.Torrent{})

	url := "magnet:?xt=urn:btih:" + h1 + "&dn=otqs-disposable-smoke"
	if s1 := m.Submit(Request{Action: Add, URL: url, Ref: "smoke-1"}); s1.Outcome != OutcomeAccepted {
		t.Fatalf("stage1 = %+v", s1)
	}
	s.set(map[string]state.Torrent{h1: torrent(h1, state.StateDownloading)})
	waitResult(t, m)

	// Replay the completed add ref: recorded result, no second backend add.
	s1 := m.Submit(Request{Action: Add, URL: url, Ref: "smoke-1"})
	if s1.Replay == nil || s1.Replay.Status != StatusConfirmed {
		t.Fatalf("replay = %+v, want recorded confirmed result", s1)
	}
	if got := b.recorded(); len(got) != 1 {
		t.Fatalf("backend calls = %v", got)
	}
}
