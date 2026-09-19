// Package state owns the daemon's normalized torrent state and the
// sync/maindata synchronization loop (Phase 0.2). All qBittorrent I/O and
// ALL merge semantics live here; the IPC layer serves committed snapshots
// and change events only. QML never sees rid/partial-merge semantics
// (ADR-0001/0005).
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

// Normalized torrent states exposed over IPC (ADR-0005). qBittorrent
// state strings are mapped here — inside the daemon.
const (
	StateDownloading = "downloading"
	StateSeeding     = "seeding"
	StatePaused      = "paused"
	StateQueued      = "queued"
	StateChecking    = "checking"
	StateError       = "error"
	StateMoving      = "moving"
	StateOther       = "other"
)

// EtaUnknown is the eta sentinel for ∞/unknown (matches qBittorrent).
const EtaUnknown = 8640000

// CategoryCapRunes bounds the category on the normalized model so wire
// frames stay inside the IPC budget (ADR-0005; security review finding).
const CategoryCapRunes = 128

// hashRe accepts v1 (40 hex) and v2 (64 hex) infohashes only. Anything
// else is treated as a malformed payload: the cycle is discarded and the
// last-known-good state kept (uncapped backend strings must never reach
// the wire frame budget).
var hashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$`)

func validHash(h string) bool { return hashRe.MatchString(h) }

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Torrent is the normalized, IPC-ready torrent item.
type Torrent struct {
	Hash      string
	Name      string
	State     string
	Progress  float64
	DlSpeed   int64
	UpSpeed   int64
	Eta       int64
	Ratio     float64
	Category  string
	Size      int64
	Completed int64
}

// State is one committed daemon state. It is immutable once published.
type State struct {
	BackendOK        bool
	AppVersion       string
	WebAPIVersion    string
	DlSpeed          int64
	UpSpeed          int64
	ConnectionStatus string
	Torrents         map[string]Torrent
	LastError        string
	// FreeSpace is qBittorrent's server_state.free_space_on_disk (free
	// space on the disk of the default save path), committed with the
	// same last-known-good discipline as the speeds and dropped on
	// degradation — nil means unknown (never reported or degraded), NOT
	// zero (ADR-0007).
	FreeSpace *int64
	// Generation increments on every committed change; it is the
	// subscription sequence space (ADR-0005).
	Generation uint64
}

// StatusLoading is State.LastError until the first sync cycle completes.
const StatusLoading = "loading"

// Change is the delta of one committed cycle, for subscription pushes.
type Change struct {
	Seq     uint64
	Changed []Torrent
	Removed []string
}

// Backend is the narrow adapter interface the syncer depends on
// (implemented by qbittorrent.Client; fakes in tests).
type Backend interface {
	Login(ctx context.Context) error
	AppVersion(ctx context.Context) (string, error)
	WebAPIVersion(ctx context.Context) (string, error)
	SyncMaindata(ctx context.Context, rid int64) (qbittorrent.Maindata, error)
}

// Options tunes the sync loop. Zero values get Phase 0.2 defaults.
type Options struct {
	Interval   time.Duration // sync interval after success (default 2s)
	MaxBackoff time.Duration // failure backoff cap (default 30s)
	FetchBg    time.Duration // per-cycle fetch timeout (default 4s)
}

func (o *Options) fill() {
	if o.Interval == 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.FetchBg == 0 {
		o.FetchBg = 4 * time.Second
	}
}

// Syncer synchronizes daemon state against qBittorrent and publishes
// committed states plus change events to subscribers.
type Syncer struct {
	backend Backend
	opts    Options
	log     *slog.Logger

	mu  sync.RWMutex
	cur State
	rid int64 // 0 = next response must be a full update
	ver bool  // versions probed this backend epoch

	subsMu sync.Mutex
	subs   map[chan Change]struct{}
}

// New creates a Syncer. Call Run to start the sync loop.
func New(backend Backend, opts Options, log *slog.Logger) *Syncer {
	opts.fill()
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{
		backend: backend,
		opts:    opts,
		log:     log,
		cur:     State{Torrents: map[string]Torrent{}, LastError: StatusLoading},
		subs:    map[chan Change]struct{}{},
	}
}

// State returns the committed state (never contacts the backend).
func (s *Syncer) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.shallowCopy()
}

// Health reports backend reachability from the last cycle.
func (s *Syncer) Health() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.BackendOK
}

// Subscribe returns the current committed state and a channel of
// subsequent changes. Registration happens under s.mu together with the
// snapshot read, so a cycle committing concurrently is either fully
// contained in the snapshot (registered after commit) or delivered as
// the first change (registered before commit) — a delta can never fall
// in between. Cancel stops delivery.
func (s *Syncer) Subscribe() (State, <-chan Change, func()) {
	ch := make(chan Change, 64)
	s.mu.Lock()
	st := s.cur.shallowCopy()
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
	s.mu.Unlock()

	cancel := func() {
		s.subsMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	return st, ch, cancel
}

// Run synchronizes until ctx is done, backing off exponentially on
// failure. Transitions are logged once, not per tick.
func (s *Syncer) Run(ctx context.Context) {
	backoff := s.opts.Interval
	for {
		s.mu.RLock()
		was := s.cur.BackendOK
		s.mu.RUnlock()

		ok := s.cycle(ctx, s.opts.FetchBg)
		if ok {
			backoff = s.opts.Interval
		} else {
			backoff *= 2
			if backoff > s.opts.MaxBackoff {
				backoff = s.opts.MaxBackoff
			}
		}
		if ok != was {
			if ok {
				s.log.Info("backend reachable")
			} else {
				s.mu.RLock()
				err := s.cur.LastError
				s.mu.RUnlock()
				s.log.Warn("backend unreachable", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// partialTorrent mirrors the fields the daemon normalizes; nil pointer =
// field absent from a delta (docs/QBITTORRENT.md: deltas carry only
// changed fields).
type partialTorrent struct {
	Name      *string  `json:"name"`
	State     *string  `json:"state"`
	Progress  *float64 `json:"progress"`
	Dlspeed   *int64   `json:"dlspeed"`
	Upspeed   *int64   `json:"upspeed"`
	Eta       *int64   `json:"eta"`
	Ratio     *float64 `json:"ratio"`
	Category  *string  `json:"category"`
	Size      *int64   `json:"size"`
	Completed *int64   `json:"completed"`
}

// cycle performs one sync. On any error or malformed payload the
// previous committed state is preserved untouched (last-known-good) and
// only the degraded flag/error update.
func (s *Syncer) cycle(ctx context.Context, timeout time.Duration) bool {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s.mu.RLock()
	rid, ver, prev := s.rid, s.ver, s.cur
	s.mu.RUnlock()

	// Login once per backend epoch (first cycle / after degradation);
	// SID expiry mid-session is handled inside the adapter (403 retry).
	if !prev.BackendOK {
		if err := s.backend.Login(cctx); err != nil {
			s.commitDegraded(err)
			return false
		}
	}

	md, err := s.backend.SyncMaindata(cctx, rid)
	if err != nil {
		s.commitDegraded(err)
		return false
	}

	// Build the next state from the previous one; nothing is committed
	// unless the whole payload decodes.
	next := State{
		Torrents: cloneTorrents(prev.Torrents),
	}
	var changed []Torrent
	var removed []string

	if md.FullUpdate {
		next.Torrents = make(map[string]Torrent, len(md.Torrents))
		for h, raw := range md.Torrents {
			if !validHash(h) {
				s.commitDegraded(errors.New("malformed full update: bad hash"))
				return false
			}
			t, err := decodeFull(raw)
			if err != nil {
				s.commitDegraded(fmt.Errorf("malformed full update for hash %d", len(h)))
				return false
			}
			t.Hash = h
			next.Torrents[h] = t
		}
		for h := range prev.Torrents {
			if _, ok := next.Torrents[h]; !ok {
				removed = append(removed, h)
			}
		}
		for _, t := range next.Torrents {
			if pt, ok := prev.Torrents[t.Hash]; !ok || pt != t {
				changed = append(changed, t)
			}
		}
		next.AppVersion, next.WebAPIVersion = prev.AppVersion, prev.WebAPIVersion
	} else {
		for h, raw := range md.Torrents {
			if !validHash(h) {
				s.commitDegraded(errors.New("malformed delta: bad hash"))
				return false
			}
			var p partialTorrent
			if err := json.Unmarshal(raw, &p); err != nil {
				s.commitDegraded(fmt.Errorf("malformed delta for hash %d", len(h)))
				return false
			}
			t := next.Torrents[h] // zero value if new
			p.apply(&t)
			t.Hash = h
			next.Torrents[h] = t
			changed = append(changed, t)
		}
		for _, h := range md.TorrentsRemoved {
			if _, ok := next.Torrents[h]; ok {
				delete(next.Torrents, h)
				removed = append(removed, h)
			}
		}
		next.AppVersion, next.WebAPIVersion = prev.AppVersion, prev.WebAPIVersion
	}

	// server_state is optional on deltas and may itself be partial:
	// only fields actually present update the state; absent fields
	// (including explicit-zero vs absent) keep the last known value.
	next.DlSpeed, next.UpSpeed, next.ConnectionStatus = prev.DlSpeed, prev.UpSpeed, prev.ConnectionStatus
	next.FreeSpace = prev.FreeSpace
	if ss := md.ServerState; ss != nil {
		if ss.DlInfoSpeed != nil {
			next.DlSpeed = *ss.DlInfoSpeed
		}
		if ss.UpInfoSpeed != nil {
			next.UpSpeed = *ss.UpInfoSpeed
		}
		if ss.ConnectionStatus != nil {
			next.ConnectionStatus = *ss.ConnectionStatus
		}
		if ss.FreeSpaceOnDisk != nil {
			v := *ss.FreeSpaceOnDisk
			next.FreeSpace = &v
		}
	}
	next.BackendOK = true

	// Version probes: once per backend epoch (after a failure or when
	// still unknown); they change only when qBittorrent restarts.
	if !ver || !prev.BackendOK || prev.AppVersion == "" {
		if app, err1 := s.backend.AppVersion(cctx); err1 == nil {
			if api, err2 := s.backend.WebAPIVersion(cctx); err2 == nil {
				next.AppVersion, next.WebAPIVersion = app, api
				s.mu.Lock()
				s.ver = true
				s.mu.Unlock()
			}
		}
		// Probe failure is non-fatal: speeds/torrents are live.
	}

	next.Generation = prev.Generation + 1
	s.mu.Lock()
	s.cur = next
	s.rid = md.RID
	s.mu.Unlock()

	if len(changed) > 0 || len(removed) > 0 || !prev.BackendOK {
		s.publish(Change{Seq: next.Generation, Changed: changed, Removed: removed})
	}
	return true
}

// commitDegraded keeps last-known-good torrents but flags the backend.
func (s *Syncer) commitDegraded(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cur
	next := prev.shallowCopy()
	next.BackendOK = false
	next.DlSpeed, next.UpSpeed = 0, 0
	// Unknown ≠ zero (ADR-0007): the daemon does not know the free
	// space while the backend is unreachable.
	next.FreeSpace = nil
	next.LastError = classify(err)
	s.cur = next
	s.rid = 0     // next success must be a full rebuild
	s.ver = false // re-probe versions on recovery
}

func (s *Syncer) publish(ch Change) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for c := range s.subs {
		select {
		case c <- ch:
		default:
			// Subscriber too slow to keep up with a full channel: its
			// view would be stale, so it is dropped; the IPC layer sees
			// the closed channel and disconnects the client, which then
			// rebuilds by resubscribing (ADR-0005 slow-consumer rule).
			delete(s.subs, c)
			close(c)
		}
	}
}

// decodeFull decodes a complete torrent object from a full update.
func decodeFull(raw json.RawMessage) (Torrent, error) {
	var p partialTorrent
	if err := json.Unmarshal(raw, &p); err != nil {
		return Torrent{}, err
	}
	var t Torrent
	p.apply(&t)
	return t, nil
}

func (p *partialTorrent) apply(t *Torrent) {
	if p.Name != nil {
		t.Name = *p.Name
	}
	if p.State != nil {
		t.State = normalizeState(*p.State)
	}
	if p.Progress != nil {
		t.Progress = *p.Progress
	}
	if p.Dlspeed != nil {
		t.DlSpeed = *p.Dlspeed
	}
	if p.Upspeed != nil {
		t.UpSpeed = *p.Upspeed
	}
	if p.Eta != nil {
		if *p.Eta >= EtaUnknown {
			t.Eta = EtaUnknown
		} else {
			t.Eta = *p.Eta
		}
	}
	if p.Ratio != nil {
		t.Ratio = *p.Ratio
	}
	if p.Category != nil {
		t.Category = capRunes(*p.Category, CategoryCapRunes)
	}
	if p.Size != nil {
		t.Size = *p.Size
	}
	if p.Completed != nil {
		t.Completed = *p.Completed
	}
}

// normalizeState maps qBittorrent state strings to the normalized set
// (wiki enumeration + 5.x stoppedUP/stoppedDL aliases). Unknown values
// map to "other" — never rejected.
func normalizeState(q string) string {
	switch q {
	case "downloading", "metaDL", "forcedDL", "stalledDL", "allocating":
		return StateDownloading
	case "uploading", "stalledUP", "forcedUP":
		return StateSeeding
	case "pausedUP", "pausedDL", "stoppedUP", "stoppedDL":
		return StatePaused
	case "queuedUP", "queuedDL":
		return StateQueued
	case "checkingUP", "checkingDL", "checkingResumeData":
		return StateChecking
	case "moving":
		return StateMoving
	case "error", "missingFiles":
		return StateError
	default:
		return StateOther
	}
}

func cloneTorrents(m map[string]Torrent) map[string]Torrent {
	out := make(map[string]Torrent, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// shallowCopy copies the state header and torrent map (values are
// immutable scalars/strings, so a map clone is a deep enough copy).
func (st State) shallowCopy() State {
	out := st
	out.Torrents = cloneTorrents(st.Torrents)
	return out
}

// classify maps adapter errors to short, secret-free class strings.
func classify(err error) string {
	switch {
	case errors.Is(err, qbittorrent.ErrBadCredentials):
		return "bad credentials"
	case errors.Is(err, qbittorrent.ErrBanned):
		return "banned"
	case errors.Is(err, qbittorrent.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, qbittorrent.ErrUnreachable):
		return "unreachable"
	default:
		return "unexpected response"
	}
}
