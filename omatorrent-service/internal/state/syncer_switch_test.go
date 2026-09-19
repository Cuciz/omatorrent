package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

// countingBackend counts logins (auth-frugality tests).
type countingBackend struct {
	mu       sync.Mutex
	loginErr error
	logins   int
	maindata qbittorrent.Maindata
}

func (b *countingBackend) Login(ctx context.Context) error {
	b.mu.Lock()
	b.logins++
	err := b.loginErr
	b.mu.Unlock()
	return err
}
func (b *countingBackend) AppVersion(ctx context.Context) (string, error)    { return "vB", nil }
func (b *countingBackend) WebAPIVersion(ctx context.Context) (string, error) { return "2.11.0", nil }
func (b *countingBackend) SyncMaindata(ctx context.Context, rid int64) (qbittorrent.Maindata, error) {
	return b.maindata, nil
}

// blockingBackend stalls SyncMaindata until released (stale-epoch test).
type blockingBackend struct {
	release chan struct{}
	once    sync.Once
}

func (b *blockingBackend) Login(ctx context.Context) error                   { return nil }
func (b *blockingBackend) AppVersion(ctx context.Context) (string, error)    { return "vA", nil }
func (b *blockingBackend) WebAPIVersion(ctx context.Context) (string, error) { return "2.11.0", nil }
func (b *blockingBackend) SyncMaindata(ctx context.Context, rid int64) (qbittorrent.Maindata, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return qbittorrent.Maindata{}, ctx.Err()
	}
	return full(1, "downloading"), nil
}

func TestSwitchBackendClearsStateAndPublishesRemovals(t *testing.T) {
	a := &fakeBackend{resps: []qbittorrent.Maindata{full(2, "downloading")}}
	s := New(a, Options{}, quietLogger())
	if ok := s.cycle(context.Background(), time.Second); !ok {
		t.Fatal("first cycle failed")
	}
	if got := len(s.State().Torrents); got != 2 {
		t.Fatalf("torrents = %d", got)
	}
	rid := func() int64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.rid
	}()
	if rid == 0 {
		t.Fatal("rid did not advance on backend A")
	}

	_, events, cancel := s.Subscribe()
	defer cancel()

	b := &countingBackend{maindata: full(1, "seeding")}
	s.SwitchBackend(b)

	st := s.State()
	if len(st.Torrents) != 0 {
		t.Fatalf("state after switch = %d torrents (A torrents must not survive)", len(st.Torrents))
	}
	if st.AppVersion != "" || st.WebAPIVersion != "" {
		t.Fatal("versions from backend A survived the switch")
	}
	if s.ridValue() != 0 {
		t.Fatal("rid not reset by the switch")
	}
	select {
	case ch := <-events:
		if len(ch.Removed) != 2 || len(ch.Changed) != 0 {
			t.Fatalf("switch change = %+v, want 2 removals", ch)
		}
	default:
		t.Fatal("switch published no removal event")
	}

	if ok := s.cycle(context.Background(), time.Second); !ok {
		t.Fatal("post-switch cycle failed")
	}
	st = s.State()
	if len(st.Torrents) != 1 {
		t.Fatalf("B torrents = %d, want 1", len(st.Torrents))
	}
	if st.AppVersion != "vB" {
		t.Fatalf("version = %q, want backend B's", st.AppVersion)
	}
}

// A cycle in flight for the old epoch is discarded at commit: a switch
// during a slow SyncMaindata can never publish A data into B (ADR-0008 §8).
func TestSwitchDiscardsStaleEpochCommits(t *testing.T) {
	block := &blockingBackend{release: make(chan struct{})}
	s := New(block, Options{}, quietLogger())

	done := make(chan bool, 1)
	go func() { done <- s.cycle(context.Background(), 5*time.Second) }()
	time.Sleep(50 * time.Millisecond) // cycle is now stalled inside SyncMaindata

	b := &countingBackend{maindata: qbittorrent.Maindata{}}
	s.SwitchBackend(b)
	close(block.release)

	if <-done {
		t.Fatal("stale-epoch cycle reported success")
	}
	st := s.State()
	if len(st.Torrents) != 0 || st.BackendOK {
		t.Fatalf("stale commit leaked into the new epoch: %d torrents, ok=%v", len(st.Torrents), st.BackendOK)
	}
}

// Sticky auth frugality: 3 consecutive bad-credential logins park the
// syncer; further cycles make NO backend contact (below qBittorrent's
// 5-attempt IP ban — ADR-0008 §6). A backend switch clears the state.
func TestStickyAuthFailureStopsBackendContact(t *testing.T) {
	b := &countingBackend{loginErr: qbittorrent.ErrBadCredentials}
	s := New(b, Options{}, quietLogger())

	for i := 0; i < StickyAuthFails; i++ {
		if ok := s.cycle(context.Background(), time.Second); ok {
			t.Fatal("cycle with rejected login reported success")
		}
	}
	if got := func() int { b.mu.Lock(); defer b.mu.Unlock(); return b.logins }(); got != StickyAuthFails {
		t.Fatalf("logins = %d, want %d", got, StickyAuthFails)
	}
	if s.State().LastError != ErrClassBadCredentials {
		t.Fatalf("last error = %q", s.State().LastError)
	}

	for i := 0; i < 3; i++ {
		if ok := s.cycle(context.Background(), time.Second); ok {
			t.Fatal("sticky cycle reported success")
		}
	}
	if got := func() int { b.mu.Lock(); defer b.mu.Unlock(); return b.logins }(); got != StickyAuthFails {
		t.Fatalf("sticky syncer still attempted logins: %d", got)
	}

	// Reconfiguration clears the sticky state.
	good := &countingBackend{maindata: full(1, "downloading")}
	s.SwitchBackend(good)
	if ok := s.cycle(context.Background(), time.Second); !ok {
		t.Fatal("post-switch cycle failed after clearing sticky state")
	}
}

// Auth backoff class: rejected-credential cycles report "auth" so Run
// uses the 30s->10min ladder; plain unreachable stays ordinary.
func TestAuthFailureBackoffClass(t *testing.T) {
	s := New(&countingBackend{loginErr: qbittorrent.ErrBadCredentials}, Options{}, quietLogger())
	s.cycle(context.Background(), time.Second)
	if s.lastCycleClass != "auth" {
		t.Fatalf("class = %q, want auth", s.lastCycleClass)
	}

	s2 := New(&fakeBackend{err: qbittorrent.ErrUnreachable}, Options{}, quietLogger())
	s2.cycle(context.Background(), time.Second)
	if s2.lastCycleClass != "" {
		t.Fatalf("unreachable class = %q, want \"\"", s2.lastCycleClass)
	}
}

func TestClassifyNewTokens(t *testing.T) {
	cases := map[error]string{
		qbittorrent.ErrTLSUntrusted:           ErrClassTLSUntrusted,
		qbittorrent.ErrTLSHostname:            ErrClassTLSHostname,
		qbittorrent.ErrCredentialsUnavailable: ErrClassCredentialsUnavailable,
		errors.New("x"):                       ErrClassUnexpected,
	}
	for err, want := range cases {
		if got := classify(err); got != want {
			t.Errorf("classify(%v) = %q, want %q", err, got, want)
		}
	}
}

// ridValue exposes the syncer's rid for switch assertions.
func (s *Syncer) ridValue() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rid
}

var _ = json.RawMessage{} // parity with the package's test imports
