// Manager orchestrates the connection lifecycle (ADR-0008): it owns
// the active profile, performs one-shot connection tests with
// temporary clients, activates configurations with backend-epoch
// switches, and derives the live connection status. Secrets are held
// only by the provider; the Manager moves them as []byte and wipes
// them after use.
package connection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/mutate"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/secrets"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// Status codes (ADR-0008 §7; wire-exposed one-to-one over IPC v1.4).
const (
	StatusConnecting         = "connecting"
	StatusConnected          = "connected"
	StatusUnreachable        = "unreachable"
	StatusAuthRequired       = "auth_required"
	StatusAuthFailed         = "auth_failed"
	StatusBanned             = "banned"
	StatusTLSUntrusted       = "tls_untrusted"
	StatusTLSHostname        = "tls_hostname"
	StatusSecretsUnavailable = "secrets_unavailable"
	StatusInsecureHTTP       = "insecure_http"
	StatusInvalidConfig      = "invalid_configuration"
	StatusBackendError       = "backend_error"
)

// Rejection codes for configure (docs/IPC.md v1.4).
const (
	RejectInvalidURL       = "invalid_url"
	RejectInsecureHTTP     = "insecure_http"
	RejectPinUnknown       = "pin_unknown"
	RejectSecretsMissing   = "secrets_unavailable"
	RejectMutationsPending = "mutations_pending"
	RejectStorageError     = "storage_error"
)

// Test budget (docs/IPC.md: bounded inline network work).
const TestBudget = 8 * time.Second

// testLoginWindow paces credential-bearing connection.test logins.
const testLoginWindow = 5 * time.Second

// offeredCacheCap bounds the fingerprint→DER cache of certificates
// offered during failed handshakes (memory-bounded trust-off flow).
const offeredCacheCap = 8

// StateSource supplies committed sync state for status derivation.
type StateSource interface {
	State() state.State
}

// SyncSwitcher is the syncer's epoch-switch entry point.
type SyncSwitcher interface {
	SwitchBackend(b state.Backend)
}

// MutationSwitcher is the mutator's guarded epoch-switch entry point:
// BeginSwitch drains (refusing while mutations are in flight and
// rejecting new registrations), CommitSwap/AbortSwitch finish it.
type MutationSwitcher interface {
	BeginSwitch() error
	CommitSwap(b mutate.Backend)
	AbortSwitch()
	SwitchBackend(b mutate.Backend) error // one-shot variant (tests)
	InFlight() int
}

// Manager owns the active connection. All public methods are safe for
// concurrent use.
type Manager struct {
	storePath string
	secrets   secrets.Provider
	stateSrc  StateSource
	syncer    SyncSwitcher
	mutator   MutationSwitcher
	log       *slog.Logger

	mu        sync.Mutex
	profile   Profile
	endpoint  Endpoint
	epoch     uint64
	valid     bool // profile loaded/validated (false = invalid_configuration)
	hasSecret bool
	current   *qbittorrent.Client // active adapter (logout-on-switch)
	// testLoginAt paces credential-bearing connection.test logins
	// (ban frugality for the settings surface — security review F4);
	// testPace is the window (tests may zero it).
	testLoginAt time.Time
	testPace    time.Duration
	offered     map[string][]byte
	offeredIO   []string // insertion order for the bounded cache
	// (Rollback state is NOT kept here: every Configure transaction
	// snapshots the currently-active profile locally at its start —
	// a long-lived prev field would restore a stale target after
	// multi-switch sequences; external review round 2, blocker 2.)
}

// LoadActive resolves the active profile: the persisted store when
// present (validated, fail-closed), else the fallback (typically
// derived from service.json or the default local endpoint).
func LoadActive(storePath string, fallback Profile) (Profile, Endpoint, error) {
	p, exists, err := LoadStore(storePath)
	if err != nil {
		return Profile{}, Endpoint{}, err
	}
	if !exists {
		p = fallback
	}
	if p.TLSMode == "" {
		p.TLSMode = TLSSystem
	}
	ep, err := p.Validate()
	if err != nil {
		if exists {
			return Profile{}, Endpoint{}, fmt.Errorf("connection: persisted profile invalid: %v", err)
		}
		return Profile{}, Endpoint{}, fmt.Errorf("connection: fallback profile invalid: %v", err)
	}
	return p, ep, nil
}

// NewManager creates the Manager and loads the persisted profile.
// missing=true (no connection.json) starts the fallback profile
// (caller-supplied, typically derived from service.json or defaults).
func NewManager(storePath string, fallback Profile, prov secrets.Provider, stateSrc StateSource, syncer SyncSwitcher, mutator MutationSwitcher, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		storePath: storePath,
		secrets:   prov,
		stateSrc:  stateSrc,
		syncer:    syncer,
		mutator:   mutator,
		log:       log,
		offered:   map[string][]byte{},
		testPace:  testLoginWindow,
	}
	p, ep, err := LoadActive(storePath, fallback)
	if err != nil {
		return nil, err
	}
	m.profile, m.endpoint, m.valid = p, ep, true

	// Secret presence is refreshed asynchronously: the provider may be
	// slow (subprocess) and startup must not block on it.
	go m.refreshSecretPresence()
	return m, nil
}

func (m *Manager) refreshSecretPresence() {
	ctx, cancel := context.WithTimeout(context.Background(), secretsOpBudget)
	defer cancel()
	_, ok, err := m.secrets.Get(ctx)
	if err != nil {
		m.log.Warn("secret store unavailable", "error", "provider error")
		m.mu.Lock()
		m.hasSecret = false
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.hasSecret = ok
	m.mu.Unlock()
}

const secretsOpBudget = 6 * time.Second

// AttachClient registers the startup adapter (built by main via
// BuildClient) so later switches can best-effort-logout it.
func (m *Manager) AttachClient(c *qbittorrent.Client) {
	m.mu.Lock()
	m.current = c
	m.mu.Unlock()
}

// Profile returns the active profile (non-secret fields only).
func (m *Manager) Profile() Profile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profile
}

// Status is the cache-served v1.4 status payload.
type Status struct {
	Configured bool
	Mode       string // local | remote
	URL        string // validated origin (non-secret)
	Host       string
	Transport  string
	Insecure   bool
	Username   string
	HasSecret  bool
	TLSMode    string
	Pin        string
	Status     string
	Detail     string
	Epoch      uint64
}

// Status derives the current connection status from the profile and
// the syncer's committed state. It never contacts the network.
func (m *Manager) Status() Status {
	m.mu.Lock()
	p, ep, epoch, valid, hasSecret := m.profile, m.endpoint, m.epoch, m.valid, m.hasSecret
	m.mu.Unlock()

	st := Status{
		Configured: valid,
		Username:   p.Username,
		HasSecret:  hasSecret,
		TLSMode:    p.TLSMode,
		Pin:        p.PinFingerprint,
		Epoch:      epoch,
		URL:        p.URL,
		Host:       truncateRunes(ep.Host, 128),
		Transport:  ep.Scheme,
	}
	if ep.IsLoopback {
		st.Mode = "local"
	} else {
		st.Mode = "remote"
	}
	// FACTUAL transport state, independent of consent: non-loopback
	// plain HTTP is insecure whether or not the user acknowledged it
	// (allow_insecure_http is permission; this is reality). Such a
	// profile can only be ACTIVE with the acknowledgement — the flag
	// can never launder reality into secure-looking output.
	st.Insecure = ep.InsecureTransport()
	if !valid {
		st.Status = StatusInvalidConfig
		st.Detail = "profile failed validation"
		return st
	}

	cur := m.stateSrc.State()
	switch {
	case cur.BackendOK:
		st.Status = StatusConnected
	default:
		st.Status, st.Detail = mapErrorClass(cur.LastError)
	}
	return st
}

// mapErrorClass maps the syncer's last-error class token to a v1.4
// status (tokens defined in internal/state's classify()).
func mapErrorClass(class string) (string, string) {
	switch class {
	case state.StatusLoading:
		return StatusConnecting, ""
	case state.ErrClassBadCredentials:
		return StatusAuthFailed, "credentials rejected (or Host validation failed on pre-5.2 backends)"
	case state.ErrClassBanned:
		return StatusBanned, "too many failed logins; waiting out the backend ban"
	case state.ErrClassCredentialsUnavailable:
		return StatusSecretsUnavailable, "secret store unavailable or locked"
	case state.ErrClassUnauthorized:
		return StatusAuthRequired, "backend requires authentication and none is configured"
	case state.ErrClassTLSUntrusted:
		return StatusTLSUntrusted, ""
	case state.ErrClassTLSHostname:
		return StatusTLSHostname, ""
	case state.ErrClassUnreachable:
		return StatusUnreachable, ""
	default:
		return StatusBackendError, ""
	}
}

// TestParams is one connection-test request (schema-validated upstream
// at the IPC layer; semantic validation happens here).
type TestParams struct {
	URL               string
	Username          string
	Password          []byte // nil = not provided
	UseStoredPassword bool
	TLSMode           string // system | pin
	Pin               string // 64 hex, required for pin
	AllowInsecureHTTP bool
}

// TestResult is the normalized one-shot test outcome.
type TestResult struct {
	OK                 bool
	Status             string
	Detail             string
	Host               string
	Transport          string
	AppVersion         string
	WebAPIVersion      string
	OfferedFingerprint string // iff a certificate was presented and rejected
}

// Test performs a one-shot probe with a temporary client. It changes
// no daemon state (beyond the bounded offered-certificate cache), runs
// no torrent mutations, and discards its cookie jar.
func (m *Manager) Test(ctx context.Context, p TestParams) TestResult {
	ctx, cancel := context.WithTimeout(ctx, TestBudget)
	defer cancel()

	ep, err := ValidateURL(p.URL)
	if err != nil {
		return TestResult{Status: StatusInvalidConfig, Detail: err.Error()}
	}
	if err := httpPolicyError(ep, p.AllowInsecureHTTP); err != nil {
		return TestResult{Status: StatusInsecureHTTP, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme,
			Detail: "plain HTTP to a non-loopback host requires explicit acknowledgement"}
	}

	if err := validateTLSRequest(p.TLSMode, p.Pin); err != nil {
		return TestResult{Status: StatusInvalidConfig, Detail: err.Error()}
	}
	tlsOpts, rerr := m.resolveTLSOptions(p.TLSMode, p.Pin)
	if rerr != nil {
		return TestResult{Status: StatusInvalidConfig, Detail: rerr.Error()}
	}

	fetcher, res := m.resolveSecret(ctx, p)
	if res.Status != "" {
		res.Host, res.Transport = truncateRunes(ep.Host, 128), ep.Scheme
		return res
	}
	// Ban frugality for the settings surface (security review F4): a
	// credential-bearing test performs a REAL login; pace them so a
	// retry loop (UI bug or hostile client) cannot walk into
	// qBittorrent's 5-attempt IP ban. One per window; excess is refused
	// without any backend contact.
	if p.Username != "" && fetcher != nil {
		m.mu.Lock()
		waiting := m.testPace > 0 && time.Since(m.testLoginAt) < m.testPace
		m.mu.Unlock()
		if waiting {
			return TestResult{Status: StatusAuthRequired, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme,
				Detail: "login attempts throttled — try again in a few seconds"}
		}
		m.mu.Lock()
		m.testLoginAt = time.Now()
		m.mu.Unlock()
	}

	client, err := qbittorrent.NewConfigurable(qbittorrent.ClientConfig{
		BaseURL: ep.NormalizedURL, Username: p.Username,
		SecretFetcher: fetcher, RequestTimeout: 4 * time.Second, TLS: tlsOpts,
	})
	if err != nil {
		return TestResult{Status: StatusInvalidConfig, Detail: err.Error(), Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme}
	}

	if p.Username != "" && fetcher != nil {
		if err := client.Login(ctx); err != nil {
			return m.classifyTestErr(err, ep)
		}
	}
	app, err := client.AppVersion(ctx)
	if err != nil {
		return m.classifyTestErr(err, ep)
	}
	api, err := client.WebAPIVersion(ctx)
	if err != nil {
		return m.classifyTestErr(err, ep)
	}
	return TestResult{OK: true, Status: StatusConnected, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme,
		AppVersion: app, WebAPIVersion: api}
}

// classifyTestErr maps adapter errors (incl. TLS failures with the
// offered certificate) to a TestResult.
func (m *Manager) classifyTestErr(err error, ep Endpoint) TestResult {
	res := TestResult{Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme}
	var tlsErr *qbittorrent.TLSError
	switch {
	case errors.As(err, &tlsErr):
		res.Status = StatusTLSUntrusted
		if errors.Is(tlsErr, qbittorrent.ErrTLSHostname) {
			res.Status = StatusTLSHostname
			res.Detail = "certificate is not valid for this host"
		}
		if tlsErr.Offered != "" {
			res.OfferedFingerprint = tlsErr.Offered
			m.cacheOffered(tlsErr.Offered, tlsErr.OfferedDER)
		}
		return res
	case errors.Is(err, qbittorrent.ErrBadCredentials):
		res.Status = StatusAuthFailed
		res.Detail = "credentials rejected (or Host validation failed on pre-5.2 backends)"
		return res
	case errors.Is(err, qbittorrent.ErrBanned):
		res.Status = StatusBanned
		res.Detail = "client IP is banned by the backend"
		return res
	case errors.Is(err, qbittorrent.ErrCredentialsUnavailable):
		res.Status = StatusSecretsUnavailable
		res.Detail = "secret store unavailable or locked"
		return res
	case errors.Is(err, qbittorrent.ErrUnauthorized):
		res.Status = StatusAuthRequired
		res.Detail = "backend requires authentication and none was provided"
		return res
	case errors.Is(err, qbittorrent.ErrUnreachable):
		res.Status = StatusUnreachable
		return res
	default:
		res.Status = StatusBackendError
		return res
	}
}

// offeredDERCap bounds one cached certificate (defense in depth against
// pathological chains; Go's handshake records are far smaller).
const offeredDERCap = 64 << 10

func (m *Manager) cacheOffered(fp string, der []byte) {
	if fp == "" || len(der) == 0 || len(der) > offeredDERCap {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.offered[fp]; ok {
		m.offered[fp] = append([]byte(nil), der...)
		return
	}
	if len(m.offeredIO) >= offeredCacheCap {
		oldest := m.offeredIO[0]
		m.offeredIO = m.offeredIO[1:]
		delete(m.offered, oldest)
	}
	m.offered[fp] = append([]byte(nil), der...)
	m.offeredIO = append(m.offeredIO, fp)
}

// validateTLSRequest is the PURE request-shape check (no Manager
// state): tls_mode token (system|pin — the file-only `ca` mode is not
// IPC-settable) and pin syntax (64 lowercase hex, required iff pin
// mode). It runs BEFORE exclusivity; state-dependent resolution is
// resolveTLSOptions under exclusivity.
func validateTLSRequest(mode, pin string) error {
	switch mode {
	case "", TLSSystem:
		return nil
	case TLSPin:
		if len(pin) != 64 {
			return fmt.Errorf("pin must be 64 hex characters")
		}
		if _, err := decodeHex(pin); err != nil {
			return fmt.Errorf("pin must be lowercase hex")
		}
		return nil
	default:
		return fmt.Errorf("unknown TLS mode")
	}
}

// resolveTLSOptions is the STATE-DEPENDENT half (offered-certificate
// cache, active-pin reuse from the CURRENT profile) — it may only run
// while this transaction holds the configure drain, so no transaction
// can derive trust material from a superseded profile. An error means
// the pin has no trusted certificate (pin_unknown).
func (m *Manager) resolveTLSOptions(mode, pin string) (qbittorrent.TLSOptions, error) {
	switch mode {
	case "", TLSSystem:
		return qbittorrent.TLSOptions{Mode: qbittorrent.TLSSystem}, nil
	case TLSPin:
		m.mu.Lock()
		der, cached := m.offered[pin]
		activePin := m.profile.PinFingerprint
		activePEM := []byte(m.profile.PinCertPEM)
		m.mu.Unlock()
		if cached {
			return qbittorrent.TLSOptions{Mode: qbittorrent.TLSPin, Pin: pin, PinCertPEM: pemEncode(der)}, nil
		}
		if pin == activePin && len(activePEM) > 0 {
			return qbittorrent.TLSOptions{Mode: qbittorrent.TLSPin, Pin: pin, PinCertPEM: activePEM}, nil
		}
		return qbittorrent.TLSOptions{}, fmt.Errorf("pin fingerprint has no trusted certificate (run connection.test first)")
	default:
		return qbittorrent.TLSOptions{}, fmt.Errorf("unknown TLS mode")
	}
}

// resolveSecret builds the secret fetcher for a test: explicit
// password, the stored secret, or anonymous (nil). The returned
// intermediate result carries a terminal failure when applicable.
func (m *Manager) resolveSecret(ctx context.Context, p TestParams) (func(context.Context) ([]byte, error), TestResult) {
	switch {
	case p.Password != nil:
		pw := p.Password
		return func(context.Context) ([]byte, error) {
			cp := append([]byte(nil), pw...)
			return cp, nil
		}, TestResult{}
	case p.UseStoredPassword:
		// Only meaningful with a username (no login happens without
		// one); skip the store round-trip and the retained copy
		// otherwise (security review F9).
		if p.Username == "" {
			return nil, TestResult{}
		}
		secret, ok, err := m.secrets.Get(ctx)
		if err != nil {
			return nil, TestResult{Status: StatusSecretsUnavailable, Detail: "secret store unavailable or locked"}
		}
		if !ok {
			return nil, TestResult{Status: StatusAuthRequired, Detail: "no stored credentials"}
		}
		defer secrets.Wipe(secret)
		return func(context.Context) ([]byte, error) {
			return append([]byte(nil), secret...), nil
		}, TestResult{}
	default:
		return nil, TestResult{} // anonymous probe (localhost bypass)
	}
}

// ConfigureParams is one activation request.
type ConfigureParams struct {
	TestParams
	SecretAction string // keep | replace | delete
}

// ConfigureResult reports activation.
type ConfigureResult struct {
	OK        bool
	Rejection string
	Epoch     uint64
	Mode      string
	Host      string
	Transport string
}

// Configure validates, applies the secret intent, persists the profile
// and switches the backend epoch (ADR-0008 §8/§9). Refused with
// mutations_pending while a mutation is in flight; a failed switch
// after the file write rolls the file back to the previous profile.
func (m *Manager) Configure(ctx context.Context, p ConfigureParams) ConfigureResult {
	// ============================================================
	// PHASE A — PURE REQUEST VALIDATION (no Manager-state reads).
	// Everything here depends only on the request itself, so it may
	// run before exclusivity. Rejections here are fully inert.
	// ============================================================
	ep, err := ValidateURL(p.URL)
	if err != nil {
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
	if err := httpPolicyError(ep, p.AllowInsecureHTTP); err != nil {
		return ConfigureResult{Rejection: RejectInsecureHTTP}
	}
	switch p.SecretAction {
	case "keep", "replace", "delete":
	default:
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
	if p.SecretAction == "replace" && len(p.Password) == 0 {
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
	// TLS request shape only (token + pin syntax): NO state reads —
	// state-dependent pin resolution happens under exclusivity below.
	if err := validateTLSRequest(p.TLSMode, p.Pin); err != nil {
		return ConfigureResult{Rejection: RejectInvalidURL}
	}

	// ============================================================
	// PHASE B — EXCLUSIVITY. From here on this transaction owns the
	// backend-switch drain: concurrent Configures are refused
	// (mutations_pending) or run strictly after this one commits, and
	// nothing may derive rollback or trust state from Manager state
	// before this point. Every failure path releases the drain via
	// the deferred guard; CommitSwap clears it on success.
	// ============================================================
	if err := m.mutator.BeginSwitch(); err != nil {
		return ConfigureResult{Rejection: RejectMutationsPending}
	}
	drainHeld := true
	defer func() {
		if drainHeld {
			m.mutator.AbortSwitch()
		}
	}()

	// Snapshot/rollback bookkeeping: what this transaction captured and
	// what it actually mutated — rollback runs only when there is
	// something to undo (and never before anything changed).
	var prevSecret []byte
	var prevExists bool
	var beforeBytes []byte
	var beforeExisted bool
	secretMutated := false
	storeMutated := false
	fail := func(code string) ConfigureResult {
		if secretMutated {
			m.rollbackSecret(prevSecret, prevExists)
		}
		if storeMutated {
			m.rollbackStoreBytes(beforeBytes, beforeExisted)
		}
		return ConfigureResult{Rejection: code}
	}

	// ============================================================
	// PHASE C — TRANSACTION SNAPSHOTS (under exclusivity).
	// ============================================================

	// Persisted profile as RAW BYTES: the exact rollback target, never
	// a re-marshal. An unpersisted fallback restores to "no file". An
	// unreadable-but-present store refuses the transaction: a rollback
	// target that cannot be captured must not proceed.
	beforeBytes, beforeExisted, err = readStoreRawFn(m.storePath)
	if err != nil {
		return ConfigureResult{Rejection: RejectStorageError}
	}

	sctx, cancel := context.WithTimeout(ctx, secretsOpBudget)
	defer cancel()

	// Secret snapshot: `keep` never touches the provider. `replace`/
	// `delete` REQUIRE a recoverable previous-state snapshot BEFORE
	// any mutation — a provider error is NEVER "no secret exists"; it
	// rejects with zero changes (the drain releases via the defer).
	switch p.SecretAction {
	case "keep":
		// no snapshot, no mutation, no rollback — ever
	case "replace", "delete":
		sec, exists, gerr := m.secrets.Get(sctx)
		if gerr != nil {
			return ConfigureResult{Rejection: RejectSecretsMissing}
		}
		prevSecret, prevExists = sec, exists
		if prevSecret != nil {
			defer secrets.Wipe(prevSecret)
		}
	}

	// ============================================================
	// PHASE D — STATE-DEPENDENT TLS RESOLUTION (under exclusivity):
	// the offered-certificate cache and active-pin reuse read CURRENT
	// Manager state, so they may only happen here.
	// ============================================================
	tlsOpts, rerr := m.resolveTLSOptions(p.TLSMode, p.Pin)
	if rerr != nil {
		return ConfigureResult{Rejection: RejectPinUnknown}
	}

	// ============================================================
	// PHASE E — THE TRANSACTION.
	// ============================================================
	next := Profile{
		URL:               ep.NormalizedURL,
		Username:          p.Username,
		TLSMode:           p.TLSMode,
		PinFingerprint:    p.Pin,
		AllowInsecureHTTP: p.AllowInsecureHTTP,
	}
	if p.TLSMode == TLSPin {
		next.PinCertPEM = string(tlsOpts.PinCertPEM)
	}
	if p.TLSMode == "" {
		next.TLSMode = TLSSystem
	}

	// Secret intent (ADR-0008 §9). A failed Store/Delete is treated as
	// atomic (nothing changed) — documented assumption; the provider
	// either errored before persisting or the write is idempotent on
	// retry, so no rollback runs for a failed mutation itself.
	switch p.SecretAction {
	case "replace":
		if serr := m.secrets.Store(sctx, p.Password); serr != nil {
			return ConfigureResult{Rejection: RejectSecretsMissing}
		}
		secrets.Wipe(p.Password)
		secretMutated = true
	case "delete":
		if derr := m.secrets.Delete(sctx); derr != nil {
			return ConfigureResult{Rejection: RejectSecretsMissing}
		}
		secretMutated = true
	}

	// Persist: the epoch switch references what is on disk.
	if werr := SaveStore(m.storePath, next); werr != nil {
		return fail(RejectStorageError)
	}
	storeMutated = true

	// Test-only injection point (nil in production): lets regression
	// tests fail a transaction deterministically AFTER persistence so
	// the rollback paths (secret + raw store restore) actually execute
	// — an at-persistence collision would block the rollback write too.
	if buildFailureHook != nil {
		if herr := buildFailureHook(next); herr != nil {
			return fail(RejectInvalidURL)
		}
	}
	client, berr := m.buildClient(next, ep)
	if berr != nil {
		return fail(RejectInvalidURL)
	}

	// Atomic activation: syncer first (its state immediately degrades —
	// any straddling submission validates against degraded state and is
	// refused), then the mutator swap commits and releases the drain.
	m.syncer.SwitchBackend(client)
	m.mutator.CommitSwap(client)
	drainHeld = false

	m.mu.Lock()
	oldClient := m.current
	m.current = client
	m.profile, m.endpoint, m.valid = next, ep, true
	m.epoch++
	switch p.SecretAction {
	case "replace":
		m.hasSecret = true
	case "delete":
		m.hasSecret = false
	}
	epoch := m.epoch
	m.mu.Unlock()

	// Best-effort logout on the superseded backend (ADR-0008 §6):
	// expires the old session server-side; no credentials in the
	// request; result ignored; never blocks the switch.
	if oldClient != nil {
		go func(c *qbittorrent.Client) {
			lctx, lcancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer lcancel()
			c.Logout(lctx)
		}(oldClient)
	}

	m.log.Info("connection configured", "host", ep.Host, "transport", ep.Scheme, "tls", next.TLSMode, "epoch", epoch)
	mode := "remote"
	if ep.IsLoopback {
		mode = "local"
	}
	return ConfigureResult{OK: true, Epoch: epoch, Mode: mode, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme}
}

// readStoreRawFn indirection for Configure (spies in concurrency
// tests prove the losing transaction never snapshots).
var readStoreRawFn = ReadStoreRaw

// buildFailureHook is a test-only seam (nil in production) that can
// fail a Configure transaction after its persistence step.
var buildFailureHook func(next Profile) error

// rollbackSecret restores the exact previous secret PRESENCE and
// VALUE after a failed replace/delete configuration (best effort;
// security review F2 + external review round 2 blocker 3). `keep`
// callers never invoke it. A restoration failure is logged with a
// classified, secret-free message and surfaces truthfully through the
// connection status (auth_failed / secrets_unavailable) — the
// original rejection code is still returned (documented decision: no
// separate IPC rollback-failure code; the daemon never reports a
// successful activation it did not perform).
func (m *Manager) rollbackSecret(prev []byte, exists bool) {
	sctx, cancel := context.WithTimeout(context.Background(), secretsOpBudget)
	defer cancel()
	switch {
	case exists && prev != nil:
		if err := m.secrets.Store(sctx, prev); err != nil {
			m.log.Error("secret rollback failed", "error", "provider error")
			return
		}
		m.mu.Lock()
		m.hasSecret = true
		m.mu.Unlock()
	case !exists:
		if err := m.secrets.Delete(sctx); err != nil {
			m.log.Error("secret rollback failed", "error", "provider error")
			return
		}
		m.mu.Lock()
		m.hasSecret = false
		m.mu.Unlock()
	}
}

// rollbackStoreBytes restores the EXACT pre-transaction persisted
// bytes after a failed activation (the runtime state was never
// swapped, so only the file needs restoring). Best effort; failures
// are logged with classified, secret-free messages.
func (m *Manager) rollbackStoreBytes(before []byte, existed bool) {
	if !existed {
		if err := removeStore(m.storePath); err != nil {
			m.log.Error("connection rollback failed", "error", err)
		}
		return
	}
	if err := SaveStoreRaw(m.storePath, before); err != nil {
		m.log.Error("connection rollback failed", "error", err)
	}
}

// BuildClient constructs the adapter for a profile (startup and
// switches). The secret fetcher resolves per login (ADR-0009);
// anonymous profiles (no username) need no fetcher. CA-mode profiles
// read the PEM at build time (fail closed when unreadable/invalid).
func BuildClient(p Profile, prov secrets.Provider) (*qbittorrent.Client, error) {
	ep, err := p.Validate()
	if err != nil {
		return nil, err
	}
	return buildClient(p, ep, prov)
}

func (m *Manager) buildClient(p Profile, ep Endpoint) (*qbittorrent.Client, error) {
	return buildClient(p, ep, m.secrets)
}

func buildClient(p Profile, ep Endpoint, prov secrets.Provider) (*qbittorrent.Client, error) {
	opts := qbittorrent.TLSOptions{Mode: p.TLSMode, Pin: p.PinFingerprint, PinCertPEM: []byte(p.PinCertPEM)}
	if p.TLSMode == TLSCA {
		pem, err := readCA(p.CAPath)
		if err != nil {
			return nil, err
		}
		opts.CAPEM = pem
	}
	if p.TLSMode == "" {
		opts.Mode = qbittorrent.TLSSystem
	}
	cfg := qbittorrent.ClientConfig{
		BaseURL:        ep.NormalizedURL,
		Username:       p.Username,
		RequestTimeout: 5 * time.Second,
		TLS:            opts,
	}
	if p.Username != "" {
		cfg.SecretFetcher = func(ctx context.Context) ([]byte, error) {
			secret, ok, err := prov.Get(ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("no stored credentials")
			}
			return secret, nil
		}
	}
	return qbittorrent.NewConfigurable(cfg)
}

// ActiveClient builds the adapter for the ACTIVE profile (used at
// daemon startup; afterwards switching goes through Configure).
func (m *Manager) ActiveClient() (*qbittorrent.Client, error) {
	m.mu.Lock()
	p, ep, valid := m.profile, m.endpoint, m.valid
	m.mu.Unlock()
	if !valid {
		return nil, errors.New("connection: active profile invalid")
	}
	return m.buildClient(p, ep)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
