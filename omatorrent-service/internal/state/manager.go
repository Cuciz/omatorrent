// Package state owns the daemon's cached view of the qBittorrent backend
// and the reconnect/backoff loop. The IPC layer serves exclusively from
// this cache; qBittorrent is never contacted synchronously from a request
// path except for a bounded first fetch before the refresher has run.
package state

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

// Backend is the narrow adapter interface the manager depends on
// (implemented by qbittorrent.Client; fakes in tests). Auth is the
// client's own concern (it re-logins on 403).
type Backend interface {
	AppVersion(ctx context.Context) (string, error)
	WebAPIVersion(ctx context.Context) (string, error)
	TransferInfo(ctx context.Context) (qbittorrent.TransferInfo, error)
	TorrentsCount(ctx context.Context) (int, error)
}

// Snapshot is the daemon's current view of the backend. Field values are
// IPC-safe: no secrets, no torrent content. (Never marshaled directly;
// cmd/omatorrent-service maps it to the IPC response shapes.)
type Snapshot struct {
	QBittorrentOK bool
	AppVersion    string
	WebAPIVersion string
	DlSpeed       int64
	UpSpeed       int64
	TorrentsTotal int
	LastError     string
}

// Options tunes the refresher. Zero values get Phase 0 defaults.
type Options struct {
	Interval   time.Duration // refresh interval after success (default 2s)
	MaxBackoff time.Duration // failure backoff cap (default 30s)
	FetchSync  time.Duration // bounded first-fetch timeout (default 3s)
	FetchBg    time.Duration // background fetch timeout (default 4s)
}

func (o *Options) fill() {
	if o.Interval == 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.FetchSync == 0 {
		o.FetchSync = 3 * time.Second
	}
	if o.FetchBg == 0 {
		o.FetchBg = 4 * time.Second
	}
}

// Manager caches backend state and refreshes it in the background.
type Manager struct {
	backend Backend
	opts    Options
	log     *slog.Logger

	mu        sync.Mutex
	snap      Snapshot
	refreshed bool
}

// New creates a Manager. Call Run to start the refresher.
func New(backend Backend, opts Options, log *slog.Logger) *Manager {
	opts.fill()
	if log == nil {
		log = slog.Default()
	}
	return &Manager{backend: backend, opts: opts, log: log}
}

// Run refreshes until ctx is done. On failure it backs off exponentially
// (Interval → MaxBackoff); on success it returns to Interval and re-probes
// the backend versions. Transitions are logged once, not per tick.
func (m *Manager) Run(ctx context.Context) {
	backoff := m.opts.Interval
	for {
		m.mu.Lock()
		was := m.snap.QBittorrentOK
		m.mu.Unlock()
		ok := m.refresh(ctx, m.opts.FetchBg)
		if ok {
			backoff = m.opts.Interval
		} else {
			backoff *= 2
			if backoff > m.opts.MaxBackoff {
				backoff = m.opts.MaxBackoff
			}
		}
		if ok != was {
			m.mu.Lock()
			if ok {
				m.log.Info("backend reachable", "app_version", m.snap.AppVersion, "webapi_version", m.snap.WebAPIVersion)
			} else {
				m.log.Warn("backend unreachable", "error", m.snap.LastError)
			}
			m.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// Snapshot returns the cached view. If the refresher has not completed a
// cycle yet, one bounded synchronous fetch is attempted (bounded well
// under the IPC 5 s write deadline; on failure the degraded snapshot is
// returned — never an error).
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	refreshed := m.refreshed
	m.mu.Unlock()
	if !refreshed {
		ctx, cancel := context.WithTimeout(context.Background(), m.opts.FetchSync)
		m.refresh(ctx, m.opts.FetchSync)
		cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

// Health reports backend reachability from the last refresh.
func (m *Manager) Health() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap.QBittorrentOK
}

// refresh performs one full fetch and updates the cache. Speeds/count
// are fetched every cycle; versions only while unknown or after the
// previous cycle failed (they change only when qBittorrent restarts).
func (m *Manager) refresh(ctx context.Context, timeout time.Duration) bool {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m.mu.Lock()
	prev := m.snap
	m.mu.Unlock()

	snap := Snapshot{}

	info, err := m.backend.TransferInfo(cctx)
	if err != nil {
		m.store(snap.withError(err))
		return false
	}
	count, err := m.backend.TorrentsCount(cctx)
	if err != nil {
		m.store(snap.withError(err))
		return false
	}
	snap.DlSpeed = info.DlSpeed
	snap.UpSpeed = info.UpSpeed
	snap.TorrentsTotal = count
	snap.QBittorrentOK = true

	if prev.AppVersion != "" && prev.QBittorrentOK {
		snap.AppVersion, snap.WebAPIVersion = prev.AppVersion, prev.WebAPIVersion
	} else {
		app, err1 := m.backend.AppVersion(cctx)
		api, err2 := m.backend.WebAPIVersion(cctx)
		if err1 == nil && err2 == nil {
			snap.AppVersion = app
			snap.WebAPIVersion = api
		}
	}
	m.store(snap)
	return true
}

func (m *Manager) store(snap Snapshot) {
	m.mu.Lock()
	m.snap = snap
	m.refreshed = true
	m.mu.Unlock()
}

// withError maps an adapter error to a short, secret-free class string.
func (s Snapshot) withError(err error) Snapshot {
	switch {
	case err == nil:
		s.LastError = ""
	case isClass(err, qbittorrent.ErrBadCredentials):
		s.LastError = "bad credentials"
	case isClass(err, qbittorrent.ErrBanned):
		s.LastError = "banned"
	case isClass(err, qbittorrent.ErrUnauthorized):
		s.LastError = "unauthorized"
	case isClass(err, qbittorrent.ErrUnreachable):
		s.LastError = "unreachable"
	default:
		s.LastError = "unexpected response"
	}
	return s
}

func isClass(err, sentinel error) bool { return errors.Is(err, sentinel) }
