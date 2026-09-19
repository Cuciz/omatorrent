// Package qbittorrent is the only qBittorrent-aware component of
// omatorrent-service (ADR-0001/0002). It speaks the WebUI API v2.
//
// Capability facts and minimum versions live in docs/QBITTORRENT.md.
// The client keeps an HTTP cookie jar across requests: sync/maindata rid
// tracking is session-scoped, and even the localhost auth bypass issues
// a session cookie (verified live, docs/QBITTORRENT.md). The cookie
// name is version-dependent (`SID` ≤ 5.1, `QBT_SID_<port>` on 5.2) and
// is never assumed — only the jar is trusted.
//
// Phase 0.5 (ADR-0008): configurable base URL (including a reverse-
// proxy path prefix), version-adaptive login (5.2 answers 204/401/403;
// ≤ 5.1 answers 200 "Ok."/"Fails."), fail-closed TLS (system roots, an
// explicit CA bundle, or certificate pinning — never a disabled
// verification), refused redirects everywhere, and password delivery
// via a fetch-per-login provider function so no copy of the secret is
// retained. Credentials are never logged or exposed in errors.
package qbittorrent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/secrets"
)

// Sentinel error classes. Auth failure and ban are distinct per
// docs/QBITTORRENT.md (wiki authentication section). ErrConflict covers
// backend refusals such as a duplicate/malformed add (HTTP 409 on
// WebAPI ≥ 5.2-era backends — live-verified). The TLS classes are
// distinct so degraded states can tell a trust problem from a hostname
// problem; ErrCredentialsUnavailable marks an unusable secret store
// (ADR-0009), which is a different condition from rejected credentials.
var (
	ErrUnreachable            = errors.New("qbittorrent: unreachable")
	ErrBadCredentials         = errors.New("qbittorrent: bad credentials")
	ErrBanned                 = errors.New("qbittorrent: temporarily banned")
	ErrUnauthorized           = errors.New("qbittorrent: unauthorized")
	ErrUnexpectedState        = errors.New("qbittorrent: unexpected response")
	ErrConflict               = errors.New("qbittorrent: conflict")
	ErrTLSUntrusted           = errors.New("qbittorrent: TLS certificate not trusted")
	ErrTLSHostname            = errors.New("qbittorrent: TLS hostname mismatch")
	ErrCredentialsUnavailable = errors.New("qbittorrent: credentials unavailable")
)

// TLS modes (ADR-0008 §4).
const (
	TLSSystem = "system" // system trust store (default)
	TLSCA     = "ca"     // explicit PEM bundle replacing the system roots
	TLSPin    = "pin"    // deliberate single-certificate trust (TOFU)
)

// TLSError decorates a TLS verification failure with the SHA-256
// fingerprint of the certificate the server actually presented. The
// fingerprint (never the certificate bytes) may be shown to the user to
// enable the explicit trust/pin flow.
type TLSError struct {
	Class      error // ErrTLSUntrusted or ErrTLSHostname
	Offered    string
	OfferedDER []byte // the presented leaf certificate (never leaves the daemon as bytes)
}

func (e *TLSError) Error() string { return e.Class.Error() }
func (e *TLSError) Unwrap() error { return e.Class }

// TLSOptions selects the trust policy. Verification (chain + hostname)
// is ON in every mode; there is no insecure option at this layer.
type TLSOptions struct {
	Mode       string
	CAPEM      []byte // ca mode: the PEM bundle (replaces system roots)
	Pin        string // pin mode: 64 lowercase hex, SHA-256 of the leaf DER
	PinCertPEM []byte // pin mode: PEM of the pinned certificate (anchor)
}

// BuildTLSConfig constructs the tls.Config for the given trust policy.
// Pin mode adds the pinned certificate to the root pool (anchor for the
// self-signed case; the system pool still serves the CA-signed case)
// AND asserts the presented leaf fingerprint — an exact-identity lock
// on top of standard verification, not a replacement for it.
func BuildTLSConfig(o TLSOptions) (*tls.Config, error) {
	switch o.Mode {
	case "", TLSSystem:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	case TLSCA:
		if len(o.CAPEM) == 0 {
			return nil, fmt.Errorf("qbittorrent: TLS mode %q requires a CA bundle", o.Mode)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(o.CAPEM) {
			return nil, fmt.Errorf("qbittorrent: CA bundle contains no certificates")
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil
	case TLSPin:
		pin, err := DecodeFingerprint(o.Pin)
		if err != nil {
			return nil, err
		}
		if len(o.PinCertPEM) == 0 {
			return nil, fmt.Errorf("qbittorrent: TLS mode %q requires the pinned certificate", o.Mode)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(o.PinCertPEM) {
			return nil, fmt.Errorf("qbittorrent: pinned certificate PEM is invalid")
		}
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return &TLSError{Class: ErrTLSUntrusted}
				}
				sum := sha256.Sum256(rawCerts[0])
				if !bytes.Equal(sum[:], pin) {
					return &TLSError{Class: ErrTLSUntrusted, Offered: hex.EncodeToString(sum[:]), OfferedDER: rawCerts[0]}
				}
				return nil
			},
		}, nil
	default:
		return nil, fmt.Errorf("qbittorrent: unknown TLS mode")
	}
}

// DecodeFingerprint parses a 64-lowercase-hex SHA-256 fingerprint.
func DecodeFingerprint(s string) ([]byte, error) {
	if len(s) != 64 {
		return nil, fmt.Errorf("qbittorrent: fingerprint must be 64 hex characters")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: fingerprint must be lowercase hex")
	}
	return b, nil
}

// Fingerprint returns the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// ServerState is the subset of sync/maindata server_state OmaTorrent
// uses. The whole object is optional on delta responses (absent when
// unchanged), and individual fields may be partial: pointer fields
// distinguish "absent" from "present zero value". This representation
// lives ONLY at the adapter boundary — the state layer merges present
// fields into plain scalars.
type ServerState struct {
	DlInfoSpeed      *int64  `json:"dl_info_speed"`
	UpInfoSpeed      *int64  `json:"up_info_speed"`
	ConnectionStatus *string `json:"connection_status"`
	FreeSpaceOnDisk  *int64  `json:"free_space_on_disk"`
}

// Maindata is one sync/maindata response. Torrents values are raw JSON:
// full objects on full updates, PARTIAL objects (only changed fields)
// on deltas — merging them is the state synchronizer's job, not the
// adapter's.
type Maindata struct {
	RID             int64                      `json:"rid"`
	FullUpdate      bool                       `json:"full_update"`
	Torrents        map[string]json.RawMessage `json:"torrents"`
	TorrentsRemoved []string                   `json:"torrents_removed"`
	ServerState     *ServerState               `json:"server_state"`
}

// ClientConfig is the full Phase 0.5 client configuration.
type ClientConfig struct {
	BaseURL string
	// Username enables authenticated mode. Empty means the qBittorrent
	// localhost auth bypass is assumed (no login is attempted).
	Username string
	// SecretFetcher supplies the password per login attempt
	// (ADR-0009); the client never retains it. Nil disables fetching
	// (anonymous mode or a static password below).
	SecretFetcher func(ctx context.Context) ([]byte, error)
	// RequestTimeout bounds each HTTP request (default 5 s).
	RequestTimeout time.Duration
	// TLS selects the trust policy (ADR-0008 §4).
	TLS TLSOptions
}

// Client is a minimal WebUI API v2 client. It is safe for concurrent
// use. A zero Username means the qBittorrent localhost auth bypass is
// assumed and no login is attempted; the cookie jar still keeps the
// bypass session (and thus rid-based incremental sync) stable.
type Client struct {
	base      *url.URL
	hc        *http.Client
	username  string
	password  string // static (tests/local); empty when SecretFetcher is used
	fetchPw   func(ctx context.Context) ([]byte, error)
	logoutURL string
}

// New creates a client with a static password. URLs with embedded
// userinfo are rejected: credentials belong to the secret provider, and
// a URL would risk reaching logs (docs/SECURITY.md).
func New(baseURL, username, password string) (*Client, error) {
	return NewConfigurable(ClientConfig{BaseURL: baseURL, Username: username, SecretFetcher: staticSecret(password)})
}

// NewConfigurable creates a client from a full configuration. The base
// URL may carry a path prefix (reverse-proxy stripping model,
// docs/QBITTORRENT.md): requests go to {base}/api/v2/....
func NewConfigurable(cfg ClientConfig) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("qbittorrent: invalid base URL (want scheme://host[:port][/path])")
	}
	if u.User != nil {
		return nil, fmt.Errorf("qbittorrent: credentials in the URL are not supported; use the secret provider")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("qbittorrent: unsupported base URL scheme (http/https only)")
	}
	tlsConf, err := BuildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: cookie jar: %w", err)
	}
	timeout := cfg.RequestTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	fetch := cfg.SecretFetcher
	if fetch == nil && cfg.Username != "" {
		return nil, fmt.Errorf("qbittorrent: authenticated mode requires a secret source")
	}
	return &Client{
		base: u,
		hc: &http.Client{Timeout: timeout, Jar: jar, Transport: &http.Transport{TLSClientConfig: tlsConf},
			// Redirects are refused (returned as-is and classified as
			// unexpected responses): a mutating POST silently converted to a
			// GET by a 302 would be reported accepted and never happen, and a
			// followed redirect could leak credentials to another origin.
			// qBittorrent's API never legitimately redirects (ADR-0008 §5).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}},
		username: cfg.Username,
		password: "",
		fetchPw:  fetch,
	}, nil
}

// staticSecret adapts a static password to the fetcher interface.
func staticSecret(pw string) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		if pw == "" {
			return nil, errors.New("no static password configured")
		}
		return []byte(pw), nil
	}
}

// BaseURL returns the normalized base URL (origin + optional path
// prefix, no trailing slash).
func (c *Client) BaseURL() string { return c.base.String() }

// Login authenticates so the cookie jar holds a session. Skipped
// (no-op) when no username is configured (localhost bypass).
//
// Neither Origin nor Referer is sent: qBittorrent 4.6→5.2 explicitly
// allows requests carrying neither header (docs/QBITTORRENT.md), and
// omitting them is more robust behind proxies than matching one.
//
// Version-adaptive contract: 5.2 answers 204 on success and 401 on
// wrong credentials; ≤ 5.1 answers 200 "Ok."/"Fails."; both answer 403
// with the ban message when the client IP is banned.
func (c *Client) Login(ctx context.Context) error {
	if c.username == "" {
		return nil
	}
	pw := c.password
	if c.fetchPw != nil {
		fetched, err := c.fetchPw(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCredentialsUnavailable, err)
		}
		if len(fetched) == 0 {
			return fmt.Errorf("%w: empty secret", ErrCredentialsUnavailable)
		}
		defer secrets.Wipe(fetched)
		pw = string(fetched)
	}
	form := url.Values{"username": {c.username}, "password": {pw}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("qbittorrent: build login: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.hc.Do(req)
	if err != nil {
		return c.transportErr(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("%w: login body: %v", ErrUnexpectedState, err)
	}

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return ErrBanned
	case resp.StatusCode == http.StatusUnauthorized:
		// 5.2: wrong credentials. ≤ 5.1 with the same code means a
		// Host/Origin validation failure — indistinguishable on the
		// wire; the combined class is documented (docs/QBITTORRENT.md).
		return ErrBadCredentials
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent:
		return fmt.Errorf("%w: login HTTP %d", ErrUnexpectedState, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil // 5.2 success; session cookie now lives in the jar
	}
	switch string(body) {
	case "Ok.":
		return nil // ≤ 5.1 success
	case "Fails.":
		return ErrBadCredentials
	default:
		return fmt.Errorf("%w: login response not recognized", ErrUnexpectedState)
	}
}

// Logout terminates the server-side session (best-effort; used on
// backend switches). 200/204 succeed; 401/403 mean the session was
// already gone and are treated as success.
func (c *Client) Logout(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+"/api/v2/auth/logout", nil)
	if err != nil {
		return fmt.Errorf("qbittorrent: build logout: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.transportErr(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusUnauthorized, http.StatusForbidden:
		return nil
	default:
		return fmt.Errorf("%w: logout HTTP %d", ErrUnexpectedState, resp.StatusCode)
	}
}

// AppVersion returns the qBittorrent application version (e.g. "v5.2.3").
// These endpoints answer with plain text, not JSON (verified live and per
// wiki).
func (c *Client) AppVersion(ctx context.Context) (string, error) {
	return c.getText(ctx, "/api/v2/app/version")
}

// WebAPIVersion returns the WebAPI version (e.g. "2.15.1"), plain text.
func (c *Client) WebAPIVersion(ctx context.Context) (string, error) {
	return c.getText(ctx, "/api/v2/app/webapiVersion")
}

// SyncMaindata fetches one sync/maindata response for the given rid.
// rid semantics (docs/QBITTORRENT.md): 0 or a rid the backend session
// does not recognize returns full_update=true with complete torrent
// objects; a matching rid returns a partial delta. The session cookie
// in the jar makes the rid stable across calls.
func (c *Client) SyncMaindata(ctx context.Context, rid int64) (Maindata, error) {
	var md Maindata
	if rid < 0 {
		rid = 0
	}
	// Response reads are memory-bounded in doFetch (64 MiB) — full
	// updates of very large torrent sets stay far below that.
	err := c.getJSON(ctx, "/api/v2/sync/maindata?rid="+strconv.FormatInt(rid, 10), &md)
	return md, err
}

// getText performs an authenticated GET and returns the trimmed plain-text
// body (the version endpoints answer plain text, not JSON).
func (c *Client) getText(ctx context.Context, path string) (string, error) {
	body, err := c.fetch(ctx, path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(body))
	// Wire-budget defense: version strings land in IPC frames capped at
	// 4096 bytes; a misbehaving endpoint returning megabytes must not
	// breach that (review finding).
	if r := []rune(v); len(r) > 64 {
		v = string(r[:64])
	}
	return v, nil
}

// ---- Mutation endpoints (Phase 0.3; semantics live-verified in
// docs/QBITTORRENT.md, mutation section) ----
//
// Every endpoint here ALWAYS targets exactly one hash and never sends the
// `all` keyword (blast-radius rule). deleteFiles is always explicit.
// The HTTP responses carry no per-torrent truth (200-empty in all
// scenarios); the mutation service confirms outcomes via sync state.

// StopTorrent stops (pauses) one torrent. Modern endpoint, WebAPI ≥ 2.11.0
// (qBittorrent 5.x) — the legacy pause/resume endpoints no longer exist
// there (live 404 on 5.2.3).
func (c *Client) StopTorrent(ctx context.Context, hash string) error {
	return c.postForm(ctx, "/api/v2/torrents/stop", url.Values{"hashes": {hash}})
}

// StartTorrent starts (resumes) one torrent. Modern endpoint, WebAPI ≥ 2.11.0.
func (c *Client) StartTorrent(ctx context.Context, hash string) error {
	return c.postForm(ctx, "/api/v2/torrents/start", url.Values{"hashes": {hash}})
}

// PauseTorrent is the pre-2.11.0 fallback for StopTorrent (qBittorrent 4.x).
func (c *Client) PauseTorrent(ctx context.Context, hash string) error {
	return c.postForm(ctx, "/api/v2/torrents/pause", url.Values{"hashes": {hash}})
}

// ResumeTorrent is the pre-2.11.0 fallback for StartTorrent (qBittorrent 4.x).
func (c *Client) ResumeTorrent(ctx context.Context, hash string) error {
	return c.postForm(ctx, "/api/v2/torrents/resume", url.Values{"hashes": {hash}})
}

// DeleteTorrent removes one torrent from the session; deleteFiles=true also
// removes the downloaded data. The boolean is ALWAYS sent explicitly — the
// daemon never relies on a backend default and never infers intent.
func (c *Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	return c.postForm(ctx, "/api/v2/torrents/delete", url.Values{
		"hashes":      {hash},
		"deleteFiles": {strconv.FormatBool(deleteFiles)},
	})
}

// AddMagnet submits one magnet URI. On ≥ 5.2.0 backends the response is
// a structured JSON object (HTTP 200, or 202 when some torrents are
// still pending) whose added_torrent_ids are returned for the caller's
// cross-check; legacy backends answer a plain body (echo nil). A
// duplicate or malformed magnet is refused with 409 (live-verified) and
// classified ErrConflict.
func (c *Client) AddMagnet(ctx context.Context, magnet string) ([]string, error) {
	body, err := c.postFormBody(ctx, "/api/v2/torrents/add", url.Values{"urls": {magnet}})
	if err != nil {
		return nil, err
	}
	var resp struct {
		AddedTorrentIDs []string `json:"added_torrent_ids"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.AddedTorrentIDs != nil {
		return resp.AddedTorrentIDs, nil
	}
	// Legacy body ("Ok." or empty on 4.x): acceptance without echo.
	return nil, nil
}

// postForm performs an authenticated form POST and discards the body.
func (c *Client) postForm(ctx context.Context, path string, form url.Values) error {
	_, err := c.postFormBody(ctx, path, form)
	return err
}

// postFormBody performs an authenticated form POST and returns the raw
// body. A 403 with configured credentials triggers one re-login and retry
// (SID expiry), mirroring fetch.
func (c *Client) postFormBody(ctx context.Context, path string, form url.Values) ([]byte, error) {
	body, err := c.doPostForm(ctx, path, form)
	if errors.Is(err, ErrUnauthorized) && c.username != "" {
		if lerr := c.Login(ctx); lerr != nil {
			return nil, lerr
		}
		return c.doPostForm(ctx, path, form)
	}
	return body, err
}

func (c *Client) doPostForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		switch resp.StatusCode {
		case http.StatusForbidden:
			return nil, ErrUnauthorized
		case http.StatusConflict:
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("%w: %s HTTP %d", ErrUnexpectedState, path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
	if err != nil {
		return nil, fmt.Errorf("%w: %s body: %v", ErrUnexpectedState, path, err)
	}
	return body, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.fetch(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s decode: %v", ErrUnexpectedState, path, err)
	}
	return nil
}

// fetch performs an authenticated GET returning the raw body. A 403 with
// configured credentials triggers one re-login and retry (SID expiry); a
// 403 without credentials is ErrUnauthorized.
func (c *Client) fetch(ctx context.Context, path string) ([]byte, error) {
	body, err := c.doFetch(ctx, path)
	if errors.Is(err, ErrUnauthorized) && c.username != "" {
		if lerr := c.Login(ctx); lerr != nil {
			return nil, lerr
		}
		return c.doFetch(ctx, path)
	}
	return body, err
}

func (c *Client) doFetch(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String()+path, nil)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: build %s: %w", path, err)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode == http.StatusForbidden {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("%w: %s HTTP %d", ErrUnexpectedState, path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<26))
	if err != nil {
		return nil, fmt.Errorf("%w: %s body: %v", ErrUnexpectedState, path, err)
	}
	return body, nil
}

// transportErr maps transport failures to sentinel-classified,
// secret-free errors: TLS verification problems first (with the
// offered certificate's fingerprint), then the protocol-mismatch hint
// (an https:// endpoint reached via http:// or vice versa — Go's two
// stable messages), then plain unreachable/timeout.
func (c *Client) transportErr(err error) error {
	// Pin-hook failures are OUR TLSError (Go wraps only standard
	// verification in tls.CertificateVerificationError); catch both.
	var own *TLSError
	if errors.As(err, &own) {
		return own
	}
	if t := classifyTLS(err); t != nil {
		return t
	}
	msg := err.Error()
	if strings.Contains(msg, "malformed HTTP response") ||
		strings.Contains(msg, "server gave HTTP response to HTTPS client") {
		return fmt.Errorf("%w: protocol mismatch (HTTPS endpoint on an http:// URL, or the reverse)", ErrUnexpectedState)
	}
	return fmt.Errorf("%w: %s", ErrUnreachable, timeoutClass(err))
}

// classifyTLS extracts a TLSError from a transport failure chain.
// Go's tls.CertificateVerificationError carries the unverified chain;
// its inner error distinguishes hostname mismatch from trust failure.
func classifyTLS(err error) *TLSError {
	var cve *tls.CertificateVerificationError
	if !errors.As(err, &cve) {
		return nil
	}
	offered := ""
	if len(cve.UnverifiedCertificates) > 0 {
		offered = Fingerprint(cve.UnverifiedCertificates[0].Raw)
	}
	var hx x509.HostnameError
	var ua x509.UnknownAuthorityError
	der := []byte(nil)
	if len(cve.UnverifiedCertificates) > 0 {
		der = cve.UnverifiedCertificates[0].Raw
	}
	switch {
	case errors.As(cve.Err, &hx):
		return &TLSError{Class: ErrTLSHostname, Offered: offered, OfferedDER: der}
	case errors.As(cve.Err, &ua):
		return &TLSError{Class: ErrTLSUntrusted, Offered: offered, OfferedDER: der}
	default:
		return &TLSError{Class: ErrTLSUntrusted, Offered: offered, OfferedDER: der}
	}
}

// timeoutClass is a short, secret-free transport summary.
func timeoutClass(err error) string {
	var nerr interface{ Timeout() bool }
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	var oerr *net.OpError
	if errors.As(err, &oerr) && oerr.Op == "dial" {
		return "connection refused or unreachable"
	}
	return "connection failed"
}
