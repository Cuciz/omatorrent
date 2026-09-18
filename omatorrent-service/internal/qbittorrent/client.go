// Package qbittorrent is the only qBittorrent-aware component of
// omatorrent-service (ADR-0001/0002). It speaks the WebUI API v2.
//
// Capability facts and minimum versions live in docs/QBITTORRENT.md; this
// Phase 0 client uses only 2.0-baseline read endpoints plus auth/login.
// Credentials are never logged or exposed in errors.
package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// TransferInfo is the subset of transfer/info OmaTorrent needs.
type TransferInfo struct {
	DlSpeed int64  `json:"dl_info_speed"`
	UpSpeed int64  `json:"up_info_speed"`
	Status  string `json:"connection_status"`
}

// Client is a minimal WebUI API v2 client. It is safe for concurrent use.
// A zero Username means the qBittorrent localhost auth bypass is assumed
// and no login is attempted.
type Client struct {
	base     *url.URL
	hc       *http.Client
	username string
	password string

	mu  sync.Mutex
	sid *http.Cookie
}

// New creates a client for the WebUI base URL (e.g. http://127.0.0.1:8080).
func New(baseURL, username, password string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("qbittorrent: invalid base URL %q", baseURL)
	}
	return &Client{
		base:     u,
		hc:       &http.Client{Timeout: 5 * time.Second},
		username: username,
		password: password,
	}, nil
}

// Login authenticates and stores the SID cookie. Skipped (no-op) when no
// username is configured (localhost bypass). The wiki requires Referer or
// Origin matching the Host header.
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
		for _, ck := range resp.Cookies() {
			if ck.Name == "SID" {
				c.mu.Lock()
				c.sid = ck
				c.mu.Unlock()
				return nil
			}
		}
		return fmt.Errorf("%w: login without SID cookie", ErrUnexpectedState)
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

// TransferInfo returns global transfer speeds and connection status.
func (c *Client) TransferInfo(ctx context.Context) (TransferInfo, error) {
	var info TransferInfo
	if err := c.get(ctx, "/api/v2/transfer/info", &info); err != nil {
		return info, err
	}
	return info, nil
}

// TorrentsCount returns the number of torrents via torrents/info length.
// (torrents/count exists on the installed version but is undocumented in
// the wiki; see docs/QBITTORRENT.md UNCERTAIN classification.)
func (c *Client) TorrentsCount(ctx context.Context) (int, error) {
	var raw []json.RawMessage
	if err := c.get(ctx, "/api/v2/torrents/info", &raw); err != nil {
		return 0, err
	}
	return len(raw), nil
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

// get performs an authenticated GET and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
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
	c.mu.Lock()
	sid := c.sid
	c.mu.Unlock()
	if sid != nil {
		req.AddCookie(sid)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
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
