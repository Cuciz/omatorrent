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

// MutationSwitcher is the mutator's guarded epoch-switch entry point.
type MutationSwitcher interface {
	SwitchBackend(b mutate.Backend) error // refuses while mutations are in flight
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
	offered   map[string][]byte
	offeredIO []string // insertion order for the bounded cache
	// prevProfile is the last successfully activated profile (rollback
	// reference if a switch fails after the file write).
	prevProfile Profile
	prevValid   bool
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
	}
	p, ep, err := LoadActive(storePath, fallback)
	if err != nil {
		return nil, err
	}
	m.profile, m.endpoint, m.valid = p, ep, true
	m.prevProfile, m.prevValid = m.profile, m.valid

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
	Host       string
	Transport  string
	Insecure   bool
	Username   string
	HasSecret  bool
	TLSMode    string
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
		Epoch:      epoch,
		Host:       truncateRunes(ep.Host, 128),
		Transport:  ep.Scheme,
	}
	if ep.IsLoopback {
		st.Mode = "local"
	} else {
		st.Mode = "remote"
	}
	st.Insecure = !ep.IsLoopback && ep.Scheme == "http" && p.AllowInsecureHTTP
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
	if !ep.IsLoopback && ep.Scheme == "http" && !p.AllowInsecureHTTP {
		return TestResult{Status: StatusInsecureHTTP, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme,
			Detail: "plain HTTP to a non-loopback host requires explicit acknowledgement"}
	}

	tlsOpts, pinFail := m.tlsOptions(p.TLSMode, p.Pin)
	if pinFail != "" {
		return TestResult{Status: StatusInvalidConfig, Detail: pinFail}
	}

	fetcher, res := m.resolveSecret(ctx, p)
	if res.Status != "" {
		res.Host, res.Transport = truncateRunes(ep.Host, 128), ep.Scheme
		return res
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

func (m *Manager) cacheOffered(fp string, der []byte) {
	if fp == "" || len(der) == 0 {
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

// tlsOptions assembles the adapter TLS options for an IPC-supplied
// mode (system|pin). The file-only `ca` mode is not IPC-settable; pin
// resolves its certificate from the offered cache or the active
// profile's stored pin (fail: ""≠nil signals the rejection detail).
func (m *Manager) tlsOptions(mode, pin string) (qbittorrent.TLSOptions, string) {
	switch mode {
	case "", TLSSystem:
		return qbittorrent.TLSOptions{Mode: qbittorrent.TLSSystem}, ""
	case TLSPin:
		m.mu.Lock()
		der, cached := m.offered[pin]
		activePin := m.profile.PinFingerprint
		activePEM := []byte(m.profile.PinCertPEM)
		m.mu.Unlock()
		if _, err := decodeHex(pin); err != nil || len(pin) != 64 {
			return qbittorrent.TLSOptions{}, "pin must be 64 lowercase hex characters"
		}
		if cached {
			return qbittorrent.TLSOptions{Mode: qbittorrent.TLSPin, Pin: pin, PinCertPEM: pemEncode(der)}, ""
		}
		if pin != "" && pin == activePin && len(activePEM) > 0 {
			return qbittorrent.TLSOptions{Mode: qbittorrent.TLSPin, Pin: pin, PinCertPEM: activePEM}, ""
		}
		return qbittorrent.TLSOptions{}, "pin fingerprint has no trusted certificate (run connection.test first)"
	default:
		return qbittorrent.TLSOptions{}, fmt.Sprintf("unknown TLS mode %q", mode)
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
		secret, ok, err := m.secrets.Get(ctx)
		if err != nil {
			return nil, TestResult{Status: StatusSecretsUnavailable, Detail: "secret store unavailable or locked"}
		}
		if !ok {
			return nil, TestResult{Status: StatusAuthRequired, Detail: "no stored credentials"}
		}
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
	ep, err := ValidateURL(p.URL)
	if err != nil {
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
	if !ep.IsLoopback && ep.Scheme == "http" && !p.AllowInsecureHTTP {
		return ConfigureResult{Rejection: RejectInsecureHTTP}
	}
	switch p.SecretAction {
	case "keep", "replace", "delete":
	default:
		return ConfigureResult{Rejection: RejectInvalidURL}
	}

	// Build the next profile. TLS `ca` is file-only; over IPC only
	// system/pin are expressible, so an existing ca_path is only
	// preserved when the new mode is pin/system-neutral... the daemon
	// writes exactly what the IPC surface selected.
	tlsOpts, pinFail := m.tlsOptions(p.TLSMode, p.Pin)
	if pinFail != "" {
		if p.TLSMode == TLSPin {
			return ConfigureResult{Rejection: RejectPinUnknown}
		}
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
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

	// Mutation-retargeting guard FIRST: no half-applied switch under a
	// concurrent submission.
	if m.mutator.InFlight() > 0 {
		return ConfigureResult{Rejection: RejectMutationsPending}
	}

	// Secret intent (ADR-0008 §9): explicit keep/replace/delete.
	sctx, cancel := context.WithTimeout(ctx, secretsOpBudget)
	defer cancel()
	switch p.SecretAction {
	case "replace":
		if len(p.Password) == 0 {
			return ConfigureResult{Rejection: RejectInvalidURL}
		}
		if err := m.secrets.Store(sctx, p.Password); err != nil {
			return ConfigureResult{Rejection: RejectSecretsMissing}
		}
		secrets.Wipe(p.Password)
	case "delete":
		if err := m.secrets.Delete(sctx); err != nil {
			return ConfigureResult{Rejection: RejectSecretsMissing}
		}
	}

	// Persist first: the epoch switch references what is on disk.
	if err := SaveStore(m.storePath, next); err != nil {
		return ConfigureResult{Rejection: RejectStorageError}
	}

	client, err := m.buildClient(next, ep)
	if err != nil {
		m.rollbackStore()
		return ConfigureResult{Rejection: RejectInvalidURL}
	}
	if err := m.mutator.SwitchBackend(client); err != nil {
		m.rollbackStore()
		return ConfigureResult{Rejection: RejectMutationsPending}
	}
	m.syncer.SwitchBackend(client)

	m.mu.Lock()
	m.prevProfile, m.prevValid = m.profile, m.valid
	m.profile, m.endpoint, m.valid = next, ep, true
	m.epoch++
	m.hasSecret = p.SecretAction == "replace" || (p.SecretAction == "keep" && m.hasSecret)
	epoch := m.epoch
	m.mu.Unlock()

	m.log.Info("connection configured", "host", ep.Host, "transport", ep.Scheme, "tls", next.TLSMode, "epoch", epoch)
	mode := "remote"
	if ep.IsLoopback {
		mode = "local"
	}
	return ConfigureResult{OK: true, Epoch: epoch, Mode: mode, Host: truncateRunes(ep.Host, 128), Transport: ep.Scheme}
}

// rollbackStore restores the previously activated profile file after a
// failed post-write switch (best effort; failures are logged).
func (m *Manager) rollbackStore() {
	m.mu.Lock()
	prev, valid := m.prevProfile, m.prevValid
	m.mu.Unlock()
	if !valid {
		// Nothing was active before: remove the file so the fallback
		// profile applies again on restart.
		if err := removeStore(m.storePath); err != nil {
			m.log.Error("connection rollback failed", "error", err)
		}
		return
	}
	if err := SaveStore(m.storePath, prev); err != nil {
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
