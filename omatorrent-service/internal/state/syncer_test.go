package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

// fakeBackend serves a scripted maindata sequence.
type fakeBackend struct {
	mu       sync.Mutex
	resps    []qbittorrent.Maindata // popped per SyncMaindata call
	err      error                  // sticky error when set (resps empty)
	failNext error                  // one-shot error
	loginErr error
	calls    int
}

func (f *fakeBackend) Login(ctx context.Context) error { return f.loginErr }

func (f *fakeBackend) AppVersion(ctx context.Context) (string, error) {
	return "v5.2.3", nil
}

func (f *fakeBackend) WebAPIVersion(ctx context.Context) (string, error) {
	return "2.15.1", nil
}

func (f *fakeBackend) SyncMaindata(ctx context.Context, rid int64) (qbittorrent.Maindata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return qbittorrent.Maindata{}, err
	}
	if len(f.resps) == 0 {
		return qbittorrent.Maindata{}, f.err
	}
	r := f.resps[0]
	f.resps = f.resps[1:]
	return r, nil
}

func full(n int, state string) qbittorrent.Maindata {
	t := make(map[string]json.RawMessage, n)
	for i := 0; i < n; i++ {
		t[mkHash(i)] = json.RawMessage(`{"name":"T` + string(rune('a'+i%26)) + string(rune('0'+i/26)) + `","state":"` + state + `","progress":0.5,"dlspeed":1,"upspeed":2,"eta":10,"ratio":1.0,"size":100,"completed":50}`)
	}
	return qbittorrent.Maindata{RID: 1, FullUpdate: true, Torrents: t,
		ServerState: &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(1), UpInfoSpeed: ptrInt64(2), ConnectionStatus: ptrString("connected")}}
}

// mkHash generates a UNIQUE valid v1 infohash (40 hex) for any index —
// required for benchmark validity beyond 256 torrents (review finding:
// the old two-nibble generator collided above 256).
func mkHash(i int) string {
	return fmt.Sprintf("%040x", i)
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func ptrInt64(v int64) *int64    { return &v }
func ptrString(v string) *string { return &v }

// runCycles drives s.cycle directly for deterministic tests.
func runCycles(t *testing.T, s *Syncer, n int) bool {
	t.Helper()
	ok := true
	for i := 0; i < n; i++ {
		if !s.cycle(context.Background(), time.Second) {
			ok = false
		}
	}
	return ok
}

func TestSyncFullUpdate(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(3, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("cycle failed")
	}
	st := s.State()
	if !st.BackendOK || len(st.Torrents) != 3 || st.DlSpeed != 1 || st.ConnectionStatus != "connected" {
		t.Fatalf("state = %+v", st)
	}
	if st.Torrents[mkHash(0)].State != StateDownloading || st.Torrents[mkHash(0)].Name == "" {
		t.Fatalf("torrent = %+v", st.Torrents[mkHash(0)])
	}
	if st.AppVersion != "v5.2.3" || st.WebAPIVersion != "2.15.1" {
		t.Fatalf("versions = %q/%q", st.AppVersion, st.WebAPIVersion)
	}
	if !s.Health() {
		t.Fatal("health false")
	}
}

func TestSyncIncrementalChangedAddedRemoved(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(2, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	// Delta: change torrent 0, add torrent 5, remove torrent 1.
	delta := qbittorrent.Maindata{
		RID: 2,
		Torrents: map[string]json.RawMessage{
			mkHash(0): json.RawMessage(`{"dlspeed":999}`), // partial: only one field
			mkHash(5): json.RawMessage(`{"name":"NEW","state":"stalledUP","progress":1,"dlspeed":0,"upspeed":7,"eta":8640000,"ratio":3,"size":10,"completed":10}`),
		},
		TorrentsRemoved: []string{mkHash(1)},
		ServerState:     &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(5), UpInfoSpeed: ptrInt64(7), ConnectionStatus: ptrString("connected")},
	}
	fb.resps = append(fb.resps, delta)
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("delta cycle failed")
	}
	st := s.State()
	if len(st.Torrents) != 2 {
		t.Fatalf("torrents = %d, want 2", len(st.Torrents))
	}
	t0 := st.Torrents[mkHash(0)]
	if t0.DlSpeed != 999 {
		t.Fatalf("changed field not merged: %+v", t0)
	}
	if t0.Name == "" || t0.State != StateDownloading {
		t.Fatalf("partial merge lost fields: %+v", t0) // name/state must survive
	}
	if st.Torrents[mkHash(5)].Name != "NEW" || st.Torrents[mkHash(5)].State != StateSeeding {
		t.Fatalf("added torrent = %+v", st.Torrents[mkHash(5)])
	}
	if _, ghost := st.Torrents[mkHash(1)]; ghost {
		t.Fatal("removed torrent still present (ghost)")
	}
	if st.DlSpeed != 5 || st.UpSpeed != 7 {
		t.Fatalf("server_state not merged: %d/%d", st.DlSpeed, st.UpSpeed)
	}
}

func TestSyncEmptyDelta(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{
		full(2, "downloading"),
		{RID: 2, Torrents: map[string]json.RawMessage{}, TorrentsRemoved: []string{}},
	}}
	s := New(fb, Options{}, quietLogger())
	runCycles(t, s, 2)
	st := s.State()
	if !st.BackendOK || len(st.Torrents) != 2 {
		t.Fatalf("state = %+v", st)
	}
}

// Malformed payloads (delta or full) must never corrupt the committed
// state (last-known-good).
func TestSyncMalformedPreservesLastKnownGood(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(2, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	fb.resps = append(fb.resps, qbittorrent.Maindata{
		RID:      2,
		Torrents: map[string]json.RawMessage{mkHash(0): json.RawMessage(`{"dlspeed":"not a number"}`)},
	})
	if s.cycle(context.Background(), time.Second) {
		t.Fatal("malformed delta reported success")
	}
	st := s.State()
	if st.BackendOK {
		t.Fatal("state still ok after malformed payload")
	}
	if len(st.Torrents) != 2 || st.Torrents[mkHash(0)].DlSpeed != 1 {
		t.Fatalf("last-known-good corrupted: %+v", st.Torrents[mkHash(0)])
	}

	// Malformed full update after recovery attempt also preserves LKG.
	fb.resps = append(fb.resps, qbittorrent.Maindata{
		RID: 3, FullUpdate: true,
		Torrents: map[string]json.RawMessage{mkHash(0): json.RawMessage(`{invalid`)},
	})
	s.cycle(context.Background(), time.Second)
	st = s.State()
	if len(st.Torrents) != 2 {
		t.Fatalf("LKG corrupted by malformed full: %d", len(st.Torrents))
	}
}

// A backend restart (rid forgotten) is signaled by full_update on the
// next response; the syncer rebuilds and diffs against the previous
// state (removed/changed computed correctly).
func TestSyncRestartRebuildAndResync(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(3, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)
	// Subscribe AFTER the first cycle so the only published event is the
	// rebuild delta under test.
	_, events, cancel := s.Subscribe()
	defer cancel()

	// Backend restarted: rid unknown → full_update with only 2 of the
	// old torrents (one vanished server-side).
	fb.resps = append(fb.resps, full(2, "seeding"))
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("rebuild cycle failed")
	}
	st := s.State()
	if len(st.Torrents) != 2 {
		t.Fatalf("rebuild = %d torrents", len(st.Torrents))
	}
	if _, ghost := st.Torrents[mkHash(2)]; ghost {
		t.Fatal("torrent removed during disconnect survived rebuild (ghost)")
	}

	// Subscribers must receive the rebuild as a delta: changed=2,
	// removed=1.
	select {
	case ev := <-events:
		if len(ev.Changed) != 2 || len(ev.Removed) != 1 {
			t.Fatalf("rebuild delta = %d changed / %d removed", len(ev.Changed), len(ev.Removed))
		}
	default:
		t.Fatal("no rebuild delta published")
	}
}

func TestSyncFailureDegradedAndRecover(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(1, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	fb.failNext = errors.New("boom")
	if s.cycle(context.Background(), time.Second) {
		t.Fatal("failed cycle reported success")
	}
	st := s.State()
	if st.BackendOK || st.LastError != "unexpected response" {
		t.Fatalf("degraded = %+v", st)
	}
	if len(st.Torrents) != 1 {
		t.Fatal("degraded cycle dropped last-known-good torrents")
	}
	if s.Health() {
		t.Fatal("health true while degraded")
	}

	// Recovery: next cycle must sync from rid 0 (full rebuild).
	fb.resps = append(fb.resps, full(2, "downloading"))
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("recovery cycle failed")
	}
	if !s.Health() {
		t.Fatal("no recovery")
	}
}

func TestSubscribeSnapshotAndDeltas(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(2, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	snap, events, cancel := s.Subscribe()
	if len(snap.Torrents) != 2 || !snap.BackendOK {
		t.Fatalf("snapshot = %+v", snap)
	}

	fb.resps = append(fb.resps, qbittorrent.Maindata{
		RID: 2, Torrents: map[string]json.RawMessage{mkHash(0): json.RawMessage(`{"dlspeed":42}`)},
	})
	s.cycle(context.Background(), time.Second)

	select {
	case ev := <-events:
		if ev.Seq == 0 || len(ev.Changed) != 1 || ev.Changed[0].DlSpeed != 42 {
			t.Fatalf("delta event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no delta event")
	}
	cancel()
}

// Concurrent readers must never observe a half-merged state.
func TestConcurrentReadsWhileSyncing(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(100, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				st := s.State()
				if len(st.Torrents) > 100 {
					panic("torn read")
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		fb.resps = append(fb.resps, qbittorrent.Maindata{
			RID:      int64(i + 2),
			Torrents: map[string]json.RawMessage{mkHash(i % 100): json.RawMessage(`{"dlspeed":1}`)},
		})
		s.cycle(context.Background(), time.Second)
	}
	close(stop)
	wg.Wait()
}

func TestRunStopsOnCancel(t *testing.T) {
	fb := &fakeBackend{}
	s := New(fb, Options{Interval: 5 * time.Millisecond}, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestStateNormalization(t *testing.T) {
	cases := map[string]string{
		"downloading": StateDownloading, "stalledDL": StateDownloading, "metaDL": StateDownloading,
		"uploading": StateSeeding, "stalledUP": StateSeeding, "forcedUP": StateSeeding,
		"pausedUP": StatePaused, "stoppedDL": StatePaused,
		"queuedDL": StateQueued, "queuedUP": StateQueued,
		"checkingUP": StateChecking, "checkingResumeData": StateChecking,
		"moving": StateMoving, "error": StateError, "missingFiles": StateError,
		"futureState": StateOther,
	}
	for q, want := range cases {
		if got := normalizeState(q); got != want {
			t.Fatalf("normalizeState(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestStateNeverContactsBackendBeforeRun(t *testing.T) {
	fb := &fakeBackend{}
	s := New(fb, Options{}, quietLogger())
	for i := 0; i < 10; i++ {
		st := s.State()
		if st.BackendOK || st.LastError != StatusLoading {
			t.Fatalf("pre-run state = %+v", st)
		}
		s.Health()
	}
	if fb.calls != 0 {
		t.Fatalf("backend contacted %d times before Run", fb.calls)
	}
}

// Bad hashes (too long / wrong charset) are malformed payloads: the
// cycle is discarded and last-known-good preserved — uncapped backend
// strings must never reach the IPC frame budget.
func TestSyncBadHashRejectedPreservesLKG(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(2, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)

	long := string(make([]byte, 0)) + "zz" + string(make([]rune, 4096))
	fb.resps = append(fb.resps, qbittorrent.Maindata{
		RID:      2,
		Torrents: map[string]json.RawMessage{long: json.RawMessage(`{"name":"x"}`)},
	})
	if s.cycle(context.Background(), time.Second) {
		t.Fatal("oversized hash accepted")
	}
	st := s.State()
	if len(st.Torrents) != 2 || st.BackendOK {
		t.Fatalf("LKG not preserved: %+v", st)
	}

	// v1 (40 hex) and v2 (64 hex) hashes are both valid.
	fb.resps = append(fb.resps, qbittorrent.Maindata{
		RID: 3,
		Torrents: map[string]json.RawMessage{
			"0123456789abcdef0123456789abcdef01234567":                         json.RawMessage(`{"name":"v1"}`),
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": json.RawMessage(`{"name":"v2"}`),
		},
	})
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("valid v1/v2 hashes rejected")
	}
}

// Categories are capped on the normalized model (wire budget defense).
func TestCategoryCapped(t *testing.T) {
	long := strings.Repeat("c", 2000)
	fb := &fakeBackend{resps: []qbittorrent.Maindata{{
		RID: 1, FullUpdate: true,
		Torrents: map[string]json.RawMessage{
			mkHash(0): json.RawMessage(`{"name":"A","category":"` + long + `"}`),
		},
	}}}
	s := New(fb, Options{}, quietLogger())
	if !s.cycle(context.Background(), time.Second) {
		t.Fatal("cycle failed")
	}
	got := s.State().Torrents[mkHash(0)].Category
	if len([]rune(got)) != CategoryCapRunes {
		t.Fatalf("category runes = %d, want %d", len([]rune(got)), CategoryCapRunes)
	}
}

// The hash generator must produce unique valid 40-hex hashes at every
// size the benchmarks and tests use (review finding: 1,000-torrent
// fixtures previously contained only 256 distinct hashes).
func TestHashGeneratorUnique(t *testing.T) {
	for _, n := range []int{10, 100, 1000, 10000} {
		seen := make(map[string]struct{}, n)
		for i := 0; i < n; i++ {
			h := mkHash(i)
			if !validHash(h) {
				t.Fatalf("n=%d i=%d: %q is not a valid 40-hex hash", n, i, h)
			}
			if _, dup := seen[h]; dup {
				t.Fatalf("n=%d i=%d: duplicate hash %q", n, i, h)
			}
			seen[h] = struct{}{}
		}
		if len(seen) != n {
			t.Fatalf("n=%d: map holds %d", n, len(seen))
		}
	}
}

// server_state merge is presence-aware: only fields present in a
// partial server_state update the state; explicit zero is a value,
// absent preserves the last known one.
func TestPartialServerStateMerge(t *testing.T) {
	fb := &fakeBackend{resps: []qbittorrent.Maindata{full(1, "downloading")}}
	s := New(fb, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)
	if st := s.State(); st.DlSpeed != 1 || st.UpSpeed != 2 || st.ConnectionStatus != "connected" {
		t.Fatalf("baseline: %+v", st)
	}

	// Partial: only dl speed present (incl. explicit zero).
	fb.resps = append(fb.resps, qbittorrent.Maindata{RID: 2,
		Torrents: map[string]json.RawMessage{}, ServerState: &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(0)}})
	s.cycle(context.Background(), time.Second)
	if st := s.State(); st.DlSpeed != 0 || st.UpSpeed != 2 || st.ConnectionStatus != "connected" {
		t.Fatalf("partial dl-only: %+v", st)
	}

	// Partial: only up speed present.
	fb.resps = append(fb.resps, qbittorrent.Maindata{RID: 3,
		Torrents: map[string]json.RawMessage{}, ServerState: &qbittorrent.ServerState{UpInfoSpeed: ptrInt64(7)}})
	s.cycle(context.Background(), time.Second)
	if st := s.State(); st.DlSpeed != 0 || st.UpSpeed != 7 {
		t.Fatalf("partial up-only: %+v", st)
	}

	// Absent server_state preserves everything.
	fb.resps = append(fb.resps, qbittorrent.Maindata{RID: 4, Torrents: map[string]json.RawMessage{}})
	s.cycle(context.Background(), time.Second)
	if st := s.State(); st.DlSpeed != 0 || st.UpSpeed != 7 || st.ConnectionStatus != "connected" {
		t.Fatalf("absent server_state: %+v", st)
	}

	// Full server_state replaces all present fields.
	fb.resps = append(fb.resps, qbittorrent.Maindata{RID: 5, Torrents: map[string]json.RawMessage{},
		ServerState: &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(9), UpInfoSpeed: ptrInt64(8), ConnectionStatus: ptrString("firewalled")}})
	s.cycle(context.Background(), time.Second)
	if st := s.State(); st.DlSpeed != 9 || st.UpSpeed != 8 || st.ConnectionStatus != "firewalled" {
		t.Fatalf("full server_state: %+v", st)
	}
}
