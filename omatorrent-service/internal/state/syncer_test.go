package state

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
		ServerState: &qbittorrent.ServerState{DlInfoSpeed: 1, UpInfoSpeed: 2, ConnectionStatus: "connected"}}
}

func mkHash(i int) string {
	h := []byte("0000000000000000000000000000000000000000")
	s := "0123456789abcdef"
	h[0] = s[i%16]
	h[1] = s[(i/16)%16]
	return string(h)
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

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
		ServerState:     &qbittorrent.ServerState{DlInfoSpeed: 5, UpInfoSpeed: 7, ConnectionStatus: "connected"},
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
