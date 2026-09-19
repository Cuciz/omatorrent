// Package connection owns the qBittorrent connection profile: strict
// daemon-side URL validation (ADR-0008 §2), the daemon-written profile
// store (§1), one-shot connection tests (§9) and backend-epoch
// switching (§8). QML never sees this package's types — the IPC layer
// maps them to wire shapes.
package connection

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Field length bounds (schema-level, fail closed).
const (
	MaxURLLen  = 2048
	MaxUserLen = 64
	MaxPathLen = 128
)

// TLS mode tokens (mirror qbittorrent's; kept here so validation does
// not depend on the adapter package).
const (
	TLSSystem = "system"
	TLSCA     = "ca"
	TLSPin    = "pin"
)

// Endpoint is the parsed, normalized form of a validated base URL.
type Endpoint struct {
	NormalizedURL string // scheme://host[:port][/path], no trailing slash
	Scheme        string // http | https
	Hostname      string // host without port
	Host          string // display-safe host label: host[:port][/path]
	Path          string // "" or "/prefix" (reverse-proxy stripping model)
	IsLoopback    bool
}

// IsLoopbackHost classifies the URL policy class: literal loopback
// IPs and the hostname "localhost" (the /etc/hosts residual is
// documented in ADR-0008 §2 — same-UID trust boundary).
func IsLoopbackHost(hostname string) bool {
	h := strings.ToLower(hostname)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ValidateURL strictly validates and normalizes a qBittorrent base URL.
// Accepted: http/https scheme, host (optional valid port, optional
// single clean path prefix). Rejected: every other scheme, embedded
// userinfo, empty host, query, fragment, dot path segments, control
// characters, backslashes, oversized inputs (ADR-0008 §2).
func ValidateURL(raw string) (Endpoint, error) {
	var ep Endpoint
	if raw == "" {
		return ep, fmt.Errorf("empty URL")
	}
	if len(raw) > MaxURLLen {
		return ep, fmt.Errorf("URL exceeds %d bytes", MaxURLLen)
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return ep, fmt.Errorf("control characters in URL")
		}
		if r == 0x20 || r == '\\' {
			return ep, fmt.Errorf("unsupported character %q in URL", r)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ep, fmt.Errorf("unparseable URL")
	}
	if u.User != nil {
		return ep, fmt.Errorf("credentials in the URL are not supported")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return ep, fmt.Errorf("unsupported scheme %q (http/https only)", u.Scheme)
	}
	if u.Host == "" {
		return ep, fmt.Errorf("empty host")
	}
	if u.RawQuery != "" {
		return ep, fmt.Errorf("query strings are not supported in the base URL")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return ep, fmt.Errorf("fragments are not supported in the base URL")
	}

	host := u.Hostname()
	if host == "" {
		return ep, fmt.Errorf("empty host")
	}
	if u.Port() != "" && u.Port() != validPort(u.Port()) {
		return ep, fmt.Errorf("invalid port")
	}
	// Normalize the path: "" or "/" → ""; otherwise a clean prefix.
	p := u.EscapedPath()
	switch {
	case p == "" || p == "/":
		p = ""
	default:
		clean := normalizePath(p)
		if clean == "" {
			return ep, fmt.Errorf("invalid path prefix")
		}
		p = clean
	}

	ep = Endpoint{
		Scheme:     u.Scheme,
		Hostname:   strings.ToLower(host),
		Path:       p,
		IsLoopback: IsLoopbackHost(host),
	}
	hostPart := u.Host
	if u.Port() == "" {
		if u.Scheme == "http" {
			hostPart += ":80"
		} else {
			hostPart += ":443"
		}
	}
	ep.NormalizedURL = u.Scheme + "://" + strings.ToLower(hostPart) + p
	ep.Host = u.Host + p
	return ep, nil
}

func validPort(p string) string {
	for _, c := range p {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if len(p) < 1 || len(p) > 5 {
		return ""
	}
	n := 0
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > 65535 {
		return ""
	}
	return p
}

// normalizePath cleans a prefix path: leading '/', no trailing '/',
// no '.'/'..' segments, bounded length. Returns "" when invalid.
func normalizePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	if len(p) > MaxPathLen {
		return ""
	}
	if strings.Contains(p[1:], "//") {
		return "" // empty segments are ambiguous, not normalized
	}
	segments := strings.Split(strings.TrimSuffix(p, "/"), "/")[1:]
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return ""
		}
	}
	return "/" + strings.Join(segments, "/")
}

// Profile is the persisted connection profile (ADR-0008 §1). All
// fields are non-secret.
type Profile struct {
	URL               string `json:"url"`
	Username          string `json:"username,omitempty"`
	TLSMode           string `json:"tls_mode"`
	CAPath            string `json:"ca_path,omitempty"`
	PinFingerprint    string `json:"pin_fingerprint,omitempty"`
	PinCertPEM        string `json:"pin_cert_pem,omitempty"`
	AllowInsecureHTTP bool   `json:"allow_insecure_http,omitempty"`
}

// DefaultProfile is the local-development default: the Phase 0
// endpoint with no credentials (localhost bypass).
func DefaultProfile() Profile {
	return Profile{URL: "http://127.0.0.1:8080", TLSMode: TLSSystem}
}

// Validate checks the profile's semantic consistency and returns its
// normalized endpoint. The HTTP policy (allow_insecure_http) is NOT
// judged here — callers decide whether an acknowledged insecure
// profile may activate (configure requires the explicit flag, the
// running daemon reports it as insecure).
func (p Profile) Validate() (Endpoint, error) {
	if len(p.Username) > MaxUserLen {
		return Endpoint{}, fmt.Errorf("username exceeds %d runes", MaxUserLen)
	}
	switch p.TLSMode {
	case "":
		p.TLSMode = TLSSystem // normalization for validation only
	case TLSSystem:
	case TLSCA:
		if p.CAPath == "" {
			return Endpoint{}, fmt.Errorf("TLS mode %q requires ca_path", TLSCA)
		}
	case TLSPin:
		if len(p.PinFingerprint) != 64 {
			return Endpoint{}, fmt.Errorf("TLS mode %q requires pin_fingerprint (64 hex)", TLSPin)
		}
		if _, err := decodeHex(p.PinFingerprint); err != nil {
			return Endpoint{}, fmt.Errorf("pin_fingerprint must be lowercase hex")
		}
		if p.PinCertPEM == "" {
			return Endpoint{}, fmt.Errorf("TLS mode %q requires pin_cert_pem", TLSPin)
		}
	default:
		return Endpoint{}, fmt.Errorf("unknown TLS mode %q", p.TLSMode)
	}
	return ValidateURL(p.URL)
}

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd-length hex")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("non-hex character")
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
