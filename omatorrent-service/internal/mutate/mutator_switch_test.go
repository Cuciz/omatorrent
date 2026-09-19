package mutate

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// recordingBackend records which backend generation served each call.
type recordingBackend struct {
	name  string
	calls *[]string
}

func (b *recordingBackend) StopTorrent(ctx context.Context, hash string) error {
	*b.calls = append(*b.calls, b.name+":stop")
	return nil
}
func (b *recordingBackend) StartTorrent(ctx context.Context, hash string) error {
	*b.calls = append(*b.calls, b.name+":start")
	return nil
}
func (b *recordingBackend) PauseTorrent(ctx context.Context, hash string) error {
	*b.calls = append(*b.calls, b.name+":pause")
	return nil
}
func (b *recordingBackend) ResumeTorrent(ctx context.Context, hash string) error {
	*b.calls = append(*b.calls, b.name+":resume")
	return nil
}
func (b *recordingBackend) AddMagnet(ctx context.Context, magnet string) ([]string, error) {
	*b.calls = append(*b.calls, b.name+":add")
	return nil, nil
}
func (b *recordingBackend) DeleteTorrent(ctx context.Context, hash string, del bool) error {
	*b.calls = append(*b.calls, b.name+":delete")
	return nil
}

// A mutation accepted on backend A that never confirms must, after a
// backend switch, settle as timeout (ambiguous) — never execute or
// confirm against backend B (ADR-0008 §8).
func TestSwitchBackendRefusesInFlightAndOrphansPendings(t *testing.T) {
	var calls []string
	a := &recordingBackend{name: "A", calls: &calls}
	b := &recordingBackend{name: "B", calls: &calls}

	src := &staticSource{st: stateWithTorrent("a1hash0000000000000000000000000000000000000")}
	m := New(a, src, Options{ReconcileWindow: 50 * time.Millisecond}, quietLog())

	// In-flight guard: a pending pause blocks the switch.
	if got := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r1"}); got.Outcome != OutcomeAccepted {
		t.Fatalf("submit = %+v", got)
	}
	if m.InFlight() != 1 {
		t.Fatalf("in flight = %d", m.InFlight())
	}
	if err := m.SwitchBackend(b); err == nil {
		t.Fatal("switch accepted with a mutation in flight")
	}

	// Let the pending expire into its ambiguous terminal state.
	time.Sleep(90 * time.Millisecond)
	m.reconcile()
	if m.InFlight() != 0 {
		t.Fatalf("pending did not settle: %d", m.InFlight())
	}

	// Now the switch succeeds and bumps the epoch.
	if err := m.SwitchBackend(b); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if m.Epoch() == 0 {
		t.Fatal("epoch not bumped")
	}

	// New mutations run against backend B only. Backend A executed
	// exactly ONCE (r1's own submission while A was active — nothing
	// re-executes or retargets after the switch).
	if got := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r2"}); got.Outcome != OutcomeAccepted {
		t.Fatalf("submit B = %+v", got)
	}
	m.reconcile()
	counts := map[string]int{}
	for _, c := range calls {
		counts[c]++
	}
	if counts["A:stop"] != 1 {
		t.Fatalf("backend A executed %d times (retarget/replay leak): %v", counts["A:stop"], calls)
	}
	if counts["B:stop"] != 1 {
		t.Fatalf("backend B executed %d times: %v", counts["B:stop"], calls)
	}
}

// Defense in depth: a pending somehow left over from an older epoch
// settles as timeout during reconcile — it can never confirm against
// the new backend's state.
func TestOrphanedPendingSettlesAsTimeout(t *testing.T) {
	var calls []string
	a := &recordingBackend{name: "A", calls: &calls}
	src := &staticSource{st: stateWithTorrent(hashA)}
	m := New(a, src, Options{ReconcileWindow: time.Hour}, quietLog()) // never expires on its own

	if got := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r1"}); got.Outcome != OutcomeAccepted {
		t.Fatalf("submit = %+v", got)
	}

	// Force a switch by clearing the in-flight guard first (simulate
	// the race the guard prevents, to prove the epoch check still
	// holds: reconcile must settle the old-epoch pending as timeout).
	m.mu.Lock()
	oldEpoch := m.epoch
	m.epoch = oldEpoch + 10 // simulate: pending is now from an older epoch
	m.mu.Unlock()

	m.reconcile()
	if m.InFlight() != 0 {
		t.Fatal("orphaned pending did not settle")
	}
	// The settled result must be timeout (ambiguous), recorded for
	// same-ref replay.
	s1 := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r1"})
	if s1.Replay == nil || s1.Replay.Status != StatusTimeout {
		t.Fatalf("replay = %+v, want recorded timeout", s1)
	}
}

// ---- helpers ----

type staticSource struct {
	st state.State
}

func (s *staticSource) State() state.State { return s.st }
func (s *staticSource) Health() bool       { return s.st.BackendOK }
func (s *staticSource) Subscribe() (state.State, <-chan state.Change, func()) {
	ch := make(chan state.Change)
	return s.st, ch, func() {}
}

const hashA = "a1hash0000000000000000000000000000000000000"

func stateWithTorrent(hash string) state.State {
	return state.State{
		BackendOK: true,
		Torrents: map[string]state.Torrent{
			hash: {Hash: hash, Name: "A", State: state.StateDownloading},
		},
		WebAPIVersion: "2.11.0",
	}
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

var _ = qbittorrent.ErrConflict // import parity

// During a drain (BeginSwitch..Commit) new submissions are refused:
// the switch cannot straddle a mutation's validation and registration
// (security review F1).
func TestDrainRejectsNewSubmissions(t *testing.T) {
	var calls []string
	a := &recordingBackend{name: "A", calls: &calls}
	src := &staticSource{st: stateWithTorrent(hashA)}
	m := New(a, src, Options{ReconcileWindow: time.Hour}, quietLog())

	if err := m.BeginSwitch(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	got := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r-drain"})
	if got.Outcome != CodeBusy {
		t.Fatalf("submit during drain = %+v, want busy", got)
	}
	if m.InFlight() != 0 {
		t.Fatal("drained submission registered anyway")
	}
	m.CommitSwap(a)
	got = m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r-after"})
	if got.Outcome != OutcomeAccepted {
		t.Fatalf("submit after commit = %+v", got)
	}
}

// AbortSwitch releases the drain without swapping.
func TestAbortSwitchReleasesDrain(t *testing.T) {
	var calls []string
	a := &recordingBackend{name: "A", calls: &calls}
	src := &staticSource{st: stateWithTorrent(hashA)}
	m := New(a, src, Options{}, quietLog())
	if err := m.BeginSwitch(); err != nil {
		t.Fatal(err)
	}
	m.AbortSwitch()
	if got := m.Submit(Request{Action: Pause, Hash: hashA, Ref: "r1"}); got.Outcome != OutcomeAccepted {
		t.Fatalf("submit after abort = %+v", got)
	}
}
