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
// docs/QBITTORRENT.md (wiki authentication section).
var (
	ErrUnreachable     = errors.New("qbittorrent: unreachable")
	ErrBadCredentials  = errors.New("qbittorrent: bad credentials")
	ErrBanned          = errors.New("qbittorrent: temporarily banned")
	ErrUnauthorized    = errors.New("qbittorrent: unauthorized")
	ErrUnexpectedState = errors.New("qbittorrent: unexpected response")
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
	return strings.TrimSpace(string(body)), nil
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
