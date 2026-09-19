// Package mutate owns torrent mutation orchestration (Phase 0.3,
// ADR-0006): validation against the daemon's committed state, backend
// submission, ref-based replay handling, and state-derived confirmation.
// Together with internal/qbittorrent it is the only qBittorrent-aware
// code in the daemon; the IPC layer sees intent, accepted/rejected and
// results only. qBittorrent mutation endpoints answer 200-empty in all
// scenarios (docs/QBITTORRENT.md) — confirmation comes EXCLUSIVELY from
// the committed sync state, never from the HTTP response.
package mutate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// Action is the normalized mutation vocabulary crossing the IPC (the
// qBittorrent stop/start-vs-pause/resume distinction never leaves the
// daemon).
type Action string

const (
	Pause  Action = "torrent.pause"
	Resume Action = "torrent.resume"
	Add    Action = "torrent.add"
	Remove Action = "torrent.remove"
)

// Rejection codes (ADR-0006 wire codes).
const (
	CodeStaleTorrent       = "stale_torrent"
	CodeInvalidURL         = "invalid_url"
	CodeDuplicate          = "duplicate"
	CodeBackendRejected    = "backend_rejected"
	CodeBackendUnavailable = "backend_unavailable"
	CodeBusy               = "busy"
	CodeRefConflict        = "ref_conflict"
)

// OutcomeAccepted is the Stage1 outcome for accepted submissions.
const OutcomeAccepted = "accepted"

// Terminal result statuses.
const (
	StatusConfirmed = "confirmed"
	StatusTimeout   = "timeout"
)

// Request is one validated IPC mutation request.
type Request struct {
	Action      Action
	Hash        string
	URL         string
	DeleteFiles bool
	Ref         string
}

// Stage1 is the synchronous answer to a mutation request. Outcome is
// OutcomeAccepted or a rejection code; Replay is non-nil when a
// completed ref was replayed — the recorded terminal result is the
// answer and no backend call happens.
type Stage1 struct {
	Outcome  string
	Mutation uint64
	Action   Action
	Hash     string
	Replay   *Result
}

// Result is the terminal, state-derived outcome of one mutation.
type Result struct {
	Mutation uint64
	Action   Action
	Hash     string
	Status   string // confirmed | timeout
}

// Backend is the mutation surface of the qBittorrent adapter.
type Backend interface {
	StopTorrent(ctx context.Context, hash string) error
	StartTorrent(ctx context.Context, hash string) error
	PauseTorrent(ctx context.Context, hash string) error
	ResumeTorrent(ctx context.Context, hash string) error
	AddMagnet(ctx context.Context, magnet string) ([]string, error)
	DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error
}

// StateSource supplies committed state and change events (state.Syncer).
type StateSource interface {
	State() state.State
	Health() bool
	Subscribe() (state.State, <-chan state.Change, func())
}

// Options tunes the mutator. Zero values get Phase 0.3 defaults.
type Options struct {
	SubmitTimeout   time.Duration // per backend submission (default 5s)
	ReconcileWindow time.Duration // state-confirmation window (default 10s)
	MaxInFlight     int           // daemon-wide concurrent submissions (default 4)
	RingSize        int           // completed refs kept for replay (default 64)
}

func (o *Options) fill() {
	if o.SubmitTimeout == 0 {
		o.SubmitTimeout = 5 * time.Second
	}
	if o.ReconcileWindow == 0 {
		o.ReconcileWindow = 10 * time.Second
	}
	if o.MaxInFlight == 0 {
		o.MaxInFlight = 4
	}
	if o.RingSize == 0 {
		o.RingSize = 64
	}
}

// Mutator orchestrates mutations. Submit is synchronous (bounded by
// SubmitTimeout); Run drives reconciliation and result publication.
type Mutator struct {
	src  StateSource
	log  *slog.Logger
	opts Options

	// backend is swapped only by SwitchBackend (guarded by mu, together
	// with epoch); Submit captures it inside the registration critical
	// section, so a mutation can never run against a backend it was not
	// registered for (ADR-0008 §8).
	//
	// epoch is the backend generation pendings are stamped with; a
	// pending left over from an older epoch settles as timeout
	// (ambiguous), never as a confirmation against the wrong backend.
	backend Backend
	epoch   uint64

	mu       sync.Mutex
	nextMut  uint64
	inflight map[string]*pending
	ring     []refRecord // bounded, oldest overwritten
	subs     map[chan Result]struct{}
}

// pending is an accepted mutation awaiting state confirmation.
type pending struct {
	mutation uint64
	action   Action
	hash     string
	url      string // recorded for ref-conflict comparison (add)
	delFiles bool
	deadline time.Time
	epoch    uint64 // backend generation this mutation belongs to
}

// refRecord is the terminal record of one completed (or stage-1
// rejected) ref, kept for replay handling.
type refRecord struct {
	ref      string
	action   Action
	hash     string
	url      string
	delFiles bool
	outcome  string // stage-1 rejection code; "" when it reached stage 2
	status   string // confirmed | timeout
	mutation uint64
}

// matches reports whether a replayed request is the same attempt as the
// recorded one. Add requests carry no hash on the wire — their identity
// is the magnet URL (the daemon derives the hash from it).
func (r *refRecord) matches(req Request) bool {
	if r.action != req.Action || r.delFiles != req.DeleteFiles {
		return false
	}
	if r.action == Add {
		return r.url == req.URL
	}
	return r.hash == req.Hash
}

func (p *pending) matches(req Request) bool {
	if p.action != req.Action || p.delFiles != req.DeleteFiles {
		return false
	}
	if p.action == Add {
		return p.url == req.URL
	}
	return p.hash == req.Hash
}

// New creates a Mutator. Call Run alongside the syncer.
func New(backend Backend, src StateSource, opts Options, log *slog.Logger) *Mutator {
	opts.fill()
	if log == nil {
		log = slog.Default()
	}
	return &Mutator{
		backend:  backend,
		src:      src,
		log:      log,
		opts:     opts,
		inflight: map[string]*pending{},
		subs:     map[chan Result]struct{}{},
	}
}

// SwitchBackend swaps the adapter under the mutation lock, refusing
// while any mutation is in flight (mutation retargeting guard,
// ADR-0008 §8). The epoch bump orphans any pending registered for the
// previous backend; reconcile settles those as timeout (ambiguous).
func (m *Mutator) SwitchBackend(b Backend) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inflight) > 0 {
		return fmt.Errorf("mutate: refusing backend switch with %d mutations in flight", len(m.inflight))
	}
	m.backend = b
	m.epoch++
	return nil
}

// ReconcileForTest drives one reconcile pass synchronously.
func (m *Mutator) ReconcileForTest() { m.reconcile() }

// InFlight reports the number of in-flight mutations.
func (m *Mutator) InFlight() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inflight)
}

// Epoch returns the current backend epoch.
func (m *Mutator) Epoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch
}

// btihRe validates the btih payload of a magnet xt parameter. NOTE:
// intentionally duplicated in internal/ipc (wire grammar) — the two
// roles differ (request field validation vs magnet parsing) but the
// hash shape must stay in sync (review note).
var btihRe = regexp.MustCompile(`^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$`)

// ParseMagnetHash extracts the (lowercased) infohash from a magnet URI.
// Base32 btih forms are a documented 0.3 limitation: hex only.
func ParseMagnetHash(u string) (string, bool) {
	if !strings.HasPrefix(u, "magnet:?") {
		return "", false
	}
	for _, kv := range strings.Split(u[len("magnet:?"):], "&") {
		if v, ok := strings.CutPrefix(kv, "xt=urn:btih:"); ok {
			if btihRe.MatchString(v) {
				return strings.ToLower(v), true
			}
		}
	}
	return "", false
}

// lookupRefLocked resolves a ref against in-flight and completed
// records. Caller holds m.mu. Returns the converged Stage1 answer when
// the ref is known (accepted-view of in-flight, recorded rejection, or
// recorded terminal replay), or ok=false when the ref is new.
func (m *Mutator) lookupRefLocked(req Request) (Stage1, bool) {
	if p, ok := m.inflight[req.Ref]; ok {
		if !p.matches(req) {
			return Stage1{Outcome: CodeRefConflict}, true
		}
		return Stage1{Outcome: OutcomeAccepted, Mutation: p.mutation, Action: p.action, Hash: p.hash}, true
	}
	for i := range m.ring {
		if m.ring[i].ref == req.Ref {
			r := m.ring[i]
			if !r.matches(req) {
				return Stage1{Outcome: CodeRefConflict}, true
			}
			if r.outcome != "" {
				return Stage1{Outcome: r.outcome}, true
			}
			return Stage1{Replay: &Result{Mutation: r.mutation, Action: r.action, Hash: r.hash, Status: r.status}}, true
		}
	}
	return Stage1{}, false
}

// Submit validates and executes one mutation request. It may block up
// to SubmitTimeout on the backend call; the context is daemon-owned
// (NOT tied to an IPC connection) so a disconnect cannot abort a
// half-submitted mutation.
func (m *Mutator) Submit(req Request) Stage1 {
	req.Hash = strings.ToLower(req.Hash)

	// Replay handling: an in-flight or completed ref never re-executes.
	// (Also re-checked atomically at registration below — a ref can move
	// from in-flight to completed while THIS call is validating against
	// state; the registration re-check closes that race.)
	m.mu.Lock()
	if s1, ok := m.lookupRefLocked(req); ok {
		m.mu.Unlock()
		return s1
	}
	// Advisory early cap check: reject obvious spam before the O(N)
	// state clone (the authoritative check happens at registration).
	if len(m.inflight) >= m.opts.MaxInFlight {
		m.mu.Unlock()
		return Stage1{Outcome: CodeBusy}
	}
	m.mu.Unlock()

	// Semantic validation against the committed state (never the
	// backend): mutations only touch torrents the daemon knows exist.
	st := m.src.State()
	var wantHash string
	switch req.Action {
	case Pause, Resume, Remove:
		if _, ok := st.Torrents[req.Hash]; !ok {
			return m.reject(req, 0, CodeStaleTorrent)
		}
	case Add:
		h, ok := ParseMagnetHash(req.URL)
		if !ok {
			return m.reject(req, 0, CodeInvalidURL)
		}
		wantHash = h
		if _, ok := st.Torrents[h]; ok {
			return m.reject(req, 0, CodeDuplicate)
		}
	default:
		return m.reject(req, 0, CodeBackendRejected)
	}
	if !st.BackendOK {
		return m.reject(req, 0, CodeBackendUnavailable)
	}

	// Register in-flight under the lock. The ref lookup happens in the
	// SAME critical section as the registration, so a ref that completed
	// (or was rejected) while this call validated can never register and
	// re-execute (review finding: registration race).
	hash := req.Hash
	if req.Action == Add {
		hash = wantHash
	}
	m.mu.Lock()
	if s1, ok := m.lookupRefLocked(req); ok {
		m.mu.Unlock()
		return s1
	}
	if len(m.inflight) >= m.opts.MaxInFlight {
		m.mu.Unlock()
		return Stage1{Outcome: CodeBusy}
	}
	m.nextMut++
	mut := m.nextMut
	backend, epoch := m.backend, m.epoch
	m.inflight[req.Ref] = &pending{
		mutation: mut,
		action:   req.Action,
		hash:     hash,
		url:      req.URL,
		delFiles: req.DeleteFiles,
		deadline: time.Now().Add(m.opts.ReconcileWindow),
		epoch:    epoch,
	}
	m.mu.Unlock()

	// Submit to the backend with a daemon-owned timeout. The backend
	// captured above is the one this mutation was registered under; a
	// concurrent SwitchBackend refuses while we are in flight.
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.SubmitTimeout)
	defer cancel()
	err := m.submitBackend(ctx, backend, req, st.WebAPIVersion)

	if err != nil {
		m.mu.Lock()
		delete(m.inflight, req.Ref)
		m.appendRingLocked(refRecord{ref: req.Ref, action: req.Action, hash: hash,
			url: req.URL, delFiles: req.DeleteFiles, outcome: classifySubmit(err)})
		m.mu.Unlock()
		m.log.Warn("mutation rejected by backend",
			"action", string(req.Action), "mutation", mut, "error", classifySubmit(err))
		return Stage1{Outcome: classifySubmit(err)}
	}

	m.log.Info("mutation accepted", "action", string(req.Action), "mutation", mut)
	return Stage1{Outcome: OutcomeAccepted, Mutation: mut, Action: req.Action, Hash: hash}
}

// submit performs the backend call, choosing endpoint generations by
// the probed WebAPI version (docs/QBITTORRENT.md: stop/start on
// ≥ 2.11.0; pause/resume were REMOVED in qBittorrent 5.x). Version
// handling: an EMPTY (unprobed) version defaults to the modern
// endpoints (reference deployments are 5.x and the syncer normally
// probes before mutations are possible); a present-but-unparseable
// version falls back to the LEGACY endpoints — both wrong guesses fail
// visibly (404 → backend_rejected), never silently.
func (m *Mutator) submitBackend(ctx context.Context, backend Backend, req Request, webapiVersion string) error {
	modern := webapiVersion == "" || webapiAtLeast(webapiVersion, 2, 11, 0)
	switch req.Action {
	case Pause:
		if modern {
			return backend.StopTorrent(ctx, req.Hash)
		}
		return backend.PauseTorrent(ctx, req.Hash)
	case Resume:
		if modern {
			return backend.StartTorrent(ctx, req.Hash)
		}
		return backend.ResumeTorrent(ctx, req.Hash)
	case Add:
		echo, err := backend.AddMagnet(ctx, req.URL)
		if err != nil {
			return err
		}
		// Cross-check the backend's echo when it provides one (≥ 5.2.0):
		// identity must match the hash the daemon parsed from the magnet.
		if len(echo) > 0 {
			h, _ := ParseMagnetHash(req.URL)
			if echo[0] != h {
				return fmt.Errorf("backend echoed foreign hash (len %d)", len(echo[0]))
			}
		}
		return nil
	case Remove:
		return backend.DeleteTorrent(ctx, req.Hash, req.DeleteFiles)
	}
	return fmt.Errorf("unknown action")
}

// reject records a stage-1 rejection for the ref (replay must not later
// execute what was refused) and returns the Stage1 answer.
func (m *Mutator) reject(req Request, mut uint64, code string) Stage1 {
	m.mu.Lock()
	m.appendRingLocked(refRecord{ref: req.Ref, action: req.Action, hash: req.Hash,
		url: req.URL, delFiles: req.DeleteFiles, outcome: code, mutation: mut})
	m.mu.Unlock()
	return Stage1{Outcome: code}
}

// Run drives reconciliation: committed-state changes (instant) plus a
// ticker (window expiry) settle in-flight mutations and publish results.
func (m *Mutator) Run(ctx context.Context) {
	_, events, cancel := m.src.Subscribe()
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				events = nil // source dropped us; ticker keeps reconciling
				continue
			}
			m.reconcile()
		case <-tick.C:
			m.reconcile()
		}
	}
}

// reconcile settles in-flight mutations against the committed state.
func (m *Mutator) reconcile() {
	st := m.src.State()
	now := time.Now()

	m.mu.Lock()
	var finished []Result
	for ref, p := range m.inflight {
		if p.epoch != m.epoch {
			// Orphaned by a backend switch (defense in depth — SwitchBackend
			// refuses while in flight, but settle safely if it ever happens):
			// the outcome is ambiguous, never confirmed against the new
			// backend (ADR-0008 §8).
			finished = append(finished, Result{Mutation: p.mutation, Action: p.action, Hash: p.hash, Status: StatusTimeout})
			m.appendRingLocked(refRecord{ref: ref, action: p.action, hash: p.hash,
				url: p.url, delFiles: p.delFiles, status: StatusTimeout, mutation: p.mutation})
			delete(m.inflight, ref)
			continue
		}
		if status, done := checkIntent(p, st); done {
			finished = append(finished, Result{Mutation: p.mutation, Action: p.action, Hash: p.hash, Status: status})
			m.appendRingLocked(refRecord{ref: ref, action: p.action, hash: p.hash,
				url: p.url, delFiles: p.delFiles, status: status, mutation: p.mutation})
			delete(m.inflight, ref)
		} else if now.After(p.deadline) {
			finished = append(finished, Result{Mutation: p.mutation, Action: p.action, Hash: p.hash, Status: StatusTimeout})
			m.appendRingLocked(refRecord{ref: ref, action: p.action, hash: p.hash,
				url: p.url, delFiles: p.delFiles, status: StatusTimeout, mutation: p.mutation})
			delete(m.inflight, ref)
		}
	}
	m.mu.Unlock()

	for _, r := range finished {
		m.log.Info("mutation result", "action", string(r.Action), "mutation", r.Mutation, "status", r.Status)
		m.publish(r)
	}
}

// checkIntent reports whether the committed state satisfies the
// mutation's intent. A degraded backend state never confirms.
func checkIntent(p *pending, st state.State) (string, bool) {
	if !st.BackendOK {
		return "", false
	}
	switch p.action {
	case Pause:
		if t, ok := st.Torrents[p.hash]; ok && t.State == state.StatePaused {
			return StatusConfirmed, true
		}
	case Resume:
		if t, ok := st.Torrents[p.hash]; ok && t.State != state.StatePaused {
			return StatusConfirmed, true
		}
	case Add:
		if _, ok := st.Torrents[p.hash]; ok {
			return StatusConfirmed, true
		}
	case Remove:
		if _, ok := st.Torrents[p.hash]; !ok {
			return StatusConfirmed, true
		}
	}
	return "", false
}

// Results subscribes to terminal results (broadcast). The channel is
// dropped (closed) for slow consumers.
func (m *Mutator) Results() (<-chan Result, func()) {
	ch := make(chan Result, 16)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
	}
	return ch, cancel
}

func (m *Mutator) publish(r Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- r:
		default:
			// Slow consumer: its view would be stale; drop it.
			delete(m.subs, ch)
			close(ch)
		}
	}
}

// appendRingLocked appends one completed record, overwriting the oldest
// beyond RingSize (bounded memory). Caller holds m.mu.
func (m *Mutator) appendRingLocked(rec refRecord) {
	if len(m.ring) >= m.opts.RingSize {
		copy(m.ring, m.ring[1:])
		m.ring[len(m.ring)-1] = rec
		return
	}
	m.ring = append(m.ring, rec)
}

// classifySubmit maps adapter errors to deterministic rejection codes.
func classifySubmit(err error) string {
	switch {
	case errors.Is(err, qbittorrent.ErrUnreachable),
		errors.Is(err, qbittorrent.ErrBanned),
		errors.Is(err, qbittorrent.ErrBadCredentials),
		errors.Is(err, qbittorrent.ErrUnauthorized):
		return CodeBackendUnavailable
	case errors.Is(err, qbittorrent.ErrConflict):
		return CodeBackendRejected
	default:
		return CodeBackendRejected
	}
}

// webapiAtLeast compares a dotted WebAPI version ("2.11.0") against a
// minimum. Unparseable components make the whole version unparseable
// (false) — callers decide the fallback.
func webapiAtLeast(v string, major, minor, patch int) bool {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) == 0 {
		return false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	if nums[0] != major {
		return nums[0] > major
	}
	if nums[1] != minor {
		return nums[1] > minor
	}
	return nums[2] >= patch
}
