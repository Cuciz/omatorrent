package qbittorrent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- certificate helpers (pure stdlib fixtures) ----

type testCA struct {
	certPEM []byte
	pool    *x509.CertPool
	key     *rsa.PrivateKey
}

func genCA(t *testing.T) *testCA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          bigNewSerial(),
		Subject:               pkix.Name{CommonName: "ot-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(caCert)
	return &testCA{certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key: key, pool: pool}
}

// big.NewIdempotent does not exist; helper to keep serials unique.
func bigNewSerial() *big.Int { return big.NewInt(time.Now().UnixNano()) }

func genLeaf(t *testing.T, ca *testCA, dnsNames []string, ips []net.IP) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(ca.certPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: bigNewSerial(),
		Subject:      pkix.Name{CommonName: "ot-test-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(leafPEM, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, der, leafPEM
}

// genSelfSigned makes a standalone (self-signed) certificate.
func genSelfSigned(t *testing.T, dnsNames []string, ips []net.IP) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: bigNewSerial(),
		Subject:      pkix.Name{CommonName: "ot-test-selfsigned"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, der
}

func newTLSServer(t *testing.T, cert tls.Certificate, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	return ts
}

// ---- version-adaptive login (5.2 vs <=5.1) ----

func TestLogin52Contract(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			if r.PostFormValue("username") != "u" || r.PostFormValue("password") != "p" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "QBT_SID_8443", Value: "s52", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(r.Cookies()) == 0 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "v5.2.3")
	}))
	defer ts.Close()

	c, err := New(ts.URL, "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login (204 contract): %v", err)
	}
	if v, err := c.AppVersion(context.Background()); err != nil || v != "v5.2.3" {
		t.Fatalf("version = %q err=%v", v, err)
	}
	// Wrong password on the 5.2 contract is 401.
	c2, _ := New(ts.URL, "u", "wrong")
	if err := c2.Login(context.Background()); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}

func TestLoginLegacyFailsBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Fails.")
	}))
	defer ts.Close()
	c, _ := New(ts.URL, "u", "wrong")
	if err := c.Login(context.Background()); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}

// ---- TLS trust modes ----

func TestTLSSystemModeRejectsSelfSignedWithFingerprint(t *testing.T) {
	// SAN covers the dialed IP, so the only verification failure is
	// trust (unknown authority) — the untrusted classification.
	cert, der := genSelfSigned(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	ts := newTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.2.3")
	})
	defer ts.Close()

	c, err := NewConfigurable(ClientConfig{BaseURL: ts.URL, TLS: TLSOptions{Mode: TLSSystem}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.AppVersion(context.Background())
	if !errors.Is(err, ErrTLSUntrusted) {
		t.Fatalf("err = %v (%T), want ErrTLSUntrusted", err, err)
	}
	var te *TLSError
	if !errors.As(err, &te) || te.Offered != Fingerprint(der) || string(te.OfferedDER) != string(der) {
		t.Fatalf("TLSError = %+v, want offered fingerprint+DER of the presented cert", te)
	}
}

func TestTLSCAMode(t *testing.T) {
	ca := genCA(t)
	cert, _, _ := genLeaf(t, ca, []string{"qbittorrent.home.arpa"}, []net.IP{net.ParseIP("127.0.0.1")})
	ts := newTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.2.3")
	})
	defer ts.Close()

	c, err := NewConfigurable(ClientConfig{BaseURL: ts.URL, TLS: TLSOptions{Mode: TLSCA, CAPEM: ca.certPEM}})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := c.AppVersion(context.Background()); err != nil || v != "v5.2.3" {
		t.Fatalf("version = %q err=%v", v, err)
	}
}

func TestTLSHostnameMismatch(t *testing.T) {
	ca := genCA(t)
	// Certificate is valid ONLY for qbittorrent.home.arpa; the client
	// dials 127.0.0.1 (httptest's host).
	cert, _, _ := genLeaf(t, ca, []string{"qbittorrent.home.arpa"}, nil)
	ts := newTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {})
	defer ts.Close()

	c, _ := NewConfigurable(ClientConfig{BaseURL: ts.URL, TLS: TLSOptions{Mode: TLSCA, CAPEM: ca.certPEM}})
	_, err := c.AppVersion(context.Background())
	if !errors.Is(err, ErrTLSHostname) {
		t.Fatalf("err = %v (%T), want ErrTLSHostname", err, err)
	}
}

// Pin mode: self-signed pinned certificate trusted via the anchor+PEM,
// fingerprint enforced (a DIFFERENT self-signed cert must fail even
// though the anchor pool trick would otherwise... it cannot: the hook
// compares the exact leaf).
func TestTLSPinMode(t *testing.T) {
	cert, der := genSelfSigned(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	ts := newTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.2.3")
	})
	defer ts.Close()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pin := Fingerprint(der)

	// Matching pin: works without any CA involvement.
	c, err := NewConfigurable(ClientConfig{BaseURL: ts.URL, TLS: TLSOptions{Mode: TLSPin, Pin: pin, PinCertPEM: pemBytes}})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := c.AppVersion(context.Background()); err != nil || v != "v5.2.3" {
		t.Fatalf("pinned version = %q err=%v", v, err)
	}

	// Pin mismatch: hard failure, never a downgrade.
	other := pin
	if other[0] == '0' {
		other = "1" + other[1:]
	} else {
		other = "0" + other[1:]
	}
	c2, _ := NewConfigurable(ClientConfig{BaseURL: ts.URL, TLS: TLSOptions{Mode: TLSPin, Pin: other, PinCertPEM: pemBytes}})
	_, err = c2.AppVersion(context.Background())
	if !errors.Is(err, ErrTLSUntrusted) {
		t.Fatalf("mismatched pin err = %v, want ErrTLSUntrusted", err)
	}
}

// ---- redirects are never followed (ADR-0008 §5) ----

func TestRedirectsRefusedEverywhere(t *testing.T) {
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits int32
		mux := http.NewServeMux()
		mux.HandleFunc("/evil", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&targetHits, 1)
			fmt.Fprint(w, "gotcha")
		})
		redirect := func(w http.ResponseWriter, r *http.Request) {
			// Same-origin target: if a redirect were ever followed, the
			// /evil hit counter makes it observable.
			http.Redirect(w, r, "/evil", code)
		}
		mux.HandleFunc("/api/v2/auth/login", redirect)
		mux.HandleFunc("/api/v2/sync/maindata", redirect)
		mux.HandleFunc("/api/v2/torrents/add", redirect)
		mux.HandleFunc("/api/v2/app/version", redirect)
		ts := httptest.NewServer(mux)
		func() {
			defer ts.Close()
			c, _ := New(ts.URL, "u", "p") // credentials: a followed redirect could leak them
			if err := c.Login(context.Background()); err == nil {
				t.Errorf("code %d: login followed/accepted a redirect", code)
			}
			if _, err := c.SyncMaindata(context.Background(), 0); err == nil {
				t.Errorf("code %d: sync followed/accepted a redirect", code)
			}
			if _, err := c.AddMagnet(context.Background(), "magnet:?xt=urn:btih:"+strings.Repeat("a", 40)); err == nil {
				t.Errorf("code %d: add followed/accepted a redirect", code)
			}
			if _, err := c.AppVersion(context.Background()); err == nil {
				t.Errorf("code %d: version followed/accepted a redirect", code)
			}
		}()
		if n := atomic.LoadInt32(&targetHits); n != 0 {
			t.Errorf("code %d: redirect target was reached %d times — credentials could leak", code, n)
		}
	}
}

// ---- reverse-proxy path prefix joins correctly ----

func TestBaseURLPathPrefix(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/qbt/api/v2/app/version":
			fmt.Fprint(w, "v5.2.3")
		case "/qbt/api/v2/app/webapiVersion":
			fmt.Fprint(w, "2.15.1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := New(ts.URL+"/qbt/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := c.AppVersion(context.Background()); err != nil || v != "v5.2.3" {
		t.Fatalf("version = %q err=%v (paths=%v)", v, err, paths)
	}
	if c.BaseURL() != ts.URL+"/qbt" {
		t.Fatalf("base = %q", c.BaseURL())
	}
}

// ---- 202 Accepted on add (5.2 pending torrents) ----

func TestAddMagnetAccepted202(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"added_torrent_ids":["`+strings.Repeat("ab", 20)+`"],"failure_count":0,"pending_count":1,"success_count":1}`)
	}))
	defer ts.Close()
	c, _ := New(ts.URL, "", "")
	echo, err := c.AddMagnet(context.Background(), "magnet:?xt=urn:btih:"+strings.Repeat("ab", 20))
	if err != nil {
		t.Fatalf("add 202: %v", err)
	}
	if len(echo) != 1 {
		t.Fatalf("echo = %v", echo)
	}
}

// ---- credentials unavailable / fetch-per-login ----

func TestCredentialsUnavailableSurfaced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	c, err := NewConfigurable(ClientConfig{
		BaseURL: ts.URL, Username: "u",
		SecretFetcher: func(context.Context) ([]byte, error) { return nil, errors.New("keyring locked") },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Login(context.Background())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("err = %v, want ErrCredentialsUnavailable", err)
	}
}

// The fetcher's buffer is zeroed after login (ADR-0009 hygiene).
func TestLoginZeroesFetchedSecret(t *testing.T) {
	secret := []byte("zerome")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.PostFormValue("password") != "zerome" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
		fmt.Fprint(w, "Ok.")
	}))
	defer ts.Close()
	c, _ := NewConfigurable(ClientConfig{
		BaseURL: ts.URL, Username: "u",
		SecretFetcher: func(context.Context) ([]byte, error) { return secret, nil },
	})
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, b := range secret {
		if b != 0 {
			t.Fatalf("secret byte %d not zeroed after login", i)
		}
	}
}

func TestLogout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/logout" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	c, _ := New(ts.URL, "", "")
	if err := c.Logout(context.Background()); err != nil {
		t.Fatalf("logout: %v", err)
	}
}

// Wrong scheme against a TLS endpoint: Go's transport reports one of
// two stable messages; both must carry the protocol-mismatch hint
// (Go's own httptest answers a graceful 400 instead, so this is tested
// at the classifier — a real Qt/qBittorrent endpoint kills the
// handshake and produces exactly these transport errors).
func TestProtocolMismatchHint(t *testing.T) {
	c, _ := New("http://127.0.0.1:1", "", "")
	for _, msg := range []string{
		`Get "http://x/": net/http: HTTP/1.x transport connection broken: malformed HTTP response "x"`,
		`Get "https://x/": http: server gave HTTP response to HTTPS client`,
	} {
		err := c.transportErr(errors.New(msg))
		if !errors.Is(err, ErrUnexpectedState) || !strings.Contains(err.Error(), "protocol mismatch") {
			t.Errorf("transportErr(%q) = %v, want protocol-mismatch hint", msg, err)
		}
	}
}
