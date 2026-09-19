// Package qbittorrent is the only qBittorrent-aware component of
// omatorrent-service (ADR-0001/0002). It speaks the WebUI API v2.
//
// Capability facts and minimum versions live in docs/QBITTORRENT.md.
// The client keeps an HTTP cookie jar across requests: sync/maindata rid
// tracking is session-scoped, and even the localhost auth bypass issues
// a session cookie (verified live, docs/QBITTORRENT.md). Credentials are
// never logged or exposed in errors.
package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sentinel error classes. Auth failure and ban are distinct per
// docs/QBITTORRENT.md (wiki authentication section). ErrConflict covers
// backend refusals such as a duplicate/malformed add (HTTP 409 on
// WebAPI ≥ 5.2-era backends — live-verified).
var (
	ErrUnreachable     = errors.New("qbittorrent: unreachable")
	ErrBadCredentials  = errors.New("qbittorrent: bad credentials")
	ErrBanned          = errors.New("qbittorrent: temporarily banned")
	ErrUnauthorized    = errors.New("qbittorrent: unauthorized")
	ErrUnexpectedState = errors.New("qbittorrent: unexpected response")
	ErrConflict        = errors.New("qbittorrent: conflict")
)

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

// Client is a minimal WebUI API v2 client. It is safe for concurrent
// use. A zero Username means the qBittorrent localhost auth bypass is
// assumed and no login is attempted; the cookie jar still keeps the
// bypass session (and thus rid-based incremental sync) stable.
type Client struct {
	base     *url.URL
	hc       *http.Client
	username string
	password string
}

// New creates a client for the WebUI base URL (e.g. http://127.0.0.1:8080).
// URLs with embedded userinfo are rejected: credentials belong in the
// config fields, and a URL would risk reaching logs (docs/SECURITY.md).
func New(baseURL, username, password string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("qbittorrent: invalid base URL (want scheme://host[:port])")
	}
	if u.User != nil {
		return nil, fmt.Errorf("qbittorrent: credentials in the URL are not supported; use the config username/password fields")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: cookie jar: %w", err)
	}
	return &Client{
		base:     u,
		hc:       &http.Client{Timeout: 5 * time.Second, Jar: jar},
		username: username,
		password: password,
	}, nil
}

// Login authenticates so the cookie jar holds a SID. Skipped (no-op)
// when no username is configured (localhost bypass). The wiki requires
// Referer or Origin matching the Host header.
func (c *Client) Login(ctx context.Context) error {
	if c.username == "" {
		return nil
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("qbittorrent: build login: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base.String())

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, classifyTransport(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("%w: login body: %v", ErrUnexpectedState, err)
	}

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return ErrBanned
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: login HTTP %d", ErrUnexpectedState, resp.StatusCode)
	}
	switch string(body) {
	case "Ok.":
		return nil // SID now lives in the cookie jar
	case "Fails.":
		return ErrBadCredentials
	default:
		return fmt.Errorf("%w: login response not recognized", ErrUnexpectedState)
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

// AddMagnet submits one magnet URI. On ≥ 5.2.0 backends the 200 response is
// a structured JSON object whose added_torrent_ids are returned for the
// caller's cross-check; legacy backends answer a plain body (echo nil).
// A duplicate or malformed magnet is refused with 409 (live-verified) and
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
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, classifyTransport(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
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
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, classifyTransport(err))
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

// classifyTransport maps transport errors to a short, secret-free summary.
func classifyTransport(err error) string {
	var nerr interface{ Timeout() bool }
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "connection failed"
}
