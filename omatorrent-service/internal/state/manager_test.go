package state

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

type fakeBackend struct {
	mu      sync.Mutex
	fail    bool
	calls   int
	failErr error
}

func (f *fakeBackend) AppVersion(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", qbittorrent.ErrUnreachable
	}
	return "v5.2.3", nil
}

func (f *fakeBackend) WebAPIVersion(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", qbittorrent.ErrUnreachable
	}
	return "2.15.1", nil
}

func (f *fakeBackend) TransferInfo(ctx context.Context) (qbittorrent.TransferInfo, error) {
	f.mu.Lock()
	f.calls++
	defer f.mu.Unlock()
	if f.fail {
		return qbittorrent.TransferInfo{}, f.failErr
	}
	return qbittorrent.TransferInfo{DlSpeed: 11, UpSpeed: 22, Status: "connected"}, nil
}

func (f *fakeBackend) TorrentsCount(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, f.failErr
	}
	return 3, nil
}

func (f *fakeBackend) setFail(fail bool, err error) {
	f.mu.Lock()
	f.fail, f.failErr = fail, err
	f.mu.Unlock()
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestSnapshotSyncFirstFetch(t *testing.T) {
	fb := &fakeBackend{}
	m := New(fb, Options{FetchSync: 100 * time.Millisecond}, quietLogger())
	snap := m.Snapshot()
	if !snap.QBittorrentOK || snap.AppVersion != "v5.2.3" || snap.WebAPIVersion != "2.15.1" {
		t.Fatalf("snap = %+v", snap)
	}
	if snap.DlSpeed != 11 || snap.UpSpeed != 22 || snap.TorrentsTotal != 3 {
		t.Fatalf("snap = %+v", snap)
	}
	if !m.Health() {
		t.Fatal("health false after successful fetch")
	}
}

func TestSnapshotDegradedWhenBackendDown(t *testing.T) {
	fb := &fakeBackend{}
	fb.setFail(true, qbittorrent.ErrUnreachable)
	m := New(fb, Options{FetchSync: 100 * time.Millisecond}, quietLogger())
	snap := m.Snapshot()
	if snap.QBittorrentOK {
		t.Fatal("snapshot reports ok while backend down")
	}
	if snap.LastError != "unreachable" {
		t.Fatalf("LastError = %q, want unreachable", snap.LastError)
	}
	if m.Health() {
		t.Fatal("health true while backend down")
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{qbittorrent.ErrBadCredentials, "bad credentials"},
		{qbittorrent.ErrBanned, "banned"},
		{qbittorrent.ErrUnauthorized, "unauthorized"},
		{qbittorrent.ErrUnreachable, "unreachable"},
		{errors.New("boom"), "unexpected response"},
		{nil, ""},
	}
	for _, tc := range cases {
		got := Snapshot{}.withError(tc.err).LastError
		if got != tc.want {
			t.Fatalf("withError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestRunRecoversAfterFailure(t *testing.T) {
	fb := &fakeBackend{}
	m := New(fb, Options{Interval: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// Healthy start.
	deadline := time.Now().Add(time.Second)
	for !m.Health() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !m.Health() {
		t.Fatal("never became healthy")
	}

	// Backend dies: health must flip false.
	fb.setFail(true, qbittorrent.ErrUnreachable)
	deadline = time.Now().Add(time.Second)
	for m.Health() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if m.Health() {
		t.Fatal("health stayed true while backend down")
	}
	snap := m.Snapshot()
	if snap.LastError != "unreachable" {
		t.Fatalf("LastError = %q", snap.LastError)
	}

	// Backend returns: health must flip true with version probes intact.
	fb.setFail(false, nil)
	deadline = time.Now().Add(2 * time.Second)
	for (!m.Health() || m.Snapshot().AppVersion == "") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	snap = m.Snapshot()
	if !snap.QBittorrentOK || snap.AppVersion != "v5.2.3" || snap.TorrentsTotal != 3 {
		t.Fatalf("recovery snap = %+v", snap)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	fb := &fakeBackend{}
	m := New(fb, Options{Interval: 5 * time.Millisecond}, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
