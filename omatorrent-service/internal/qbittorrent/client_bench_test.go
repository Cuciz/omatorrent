package qbittorrent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Benchmarks (Phase 0.5 §29): CPU-side cost of one authenticated
// sync cycle (login + maindata + version probes) over plain HTTP vs
// TLS with a pinned certificate, against LOCAL fixtures. These numbers
// isolate CPU/auth/TLS-setup overhead — they say NOTHING about real
// WAN latency (localhost loopback), and must not be reported as such.

func benchHTTPServer(b *testing.B) *httptest.Server {
	b.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "bench", Path: "/"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/sync/maindata":
			w.Write([]byte(`{"rid":1,"full_update":true,"torrents":{},"server_state":{"dl_info_speed":0,"up_info_speed":0}}`))
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5.2.3")
		case "/api/v2/app/webapiVersion":
			fmt.Fprint(w, "2.15.1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func selfSignedBenchCert(b *testing.B) (tls.Certificate, []byte) {
	b.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		b.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		b.Fatal(err)
	}
	return pair, der
}

func benchCycle(b *testing.B, c *Client) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Login(ctx); err != nil {
			b.Fatal(err)
		}
		if _, err := c.SyncMaindata(ctx, 0); err != nil {
			b.Fatal(err)
		}
		if _, err := c.AppVersion(ctx); err != nil {
			b.Fatal(err)
		}
		if _, err := c.WebAPIVersion(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuthCycleLocalHTTP(b *testing.B) {
	ts := benchHTTPServer(b)
	defer ts.Close()
	c, err := New(ts.URL, "u", "p")
	if err != nil {
		b.Fatal(err)
	}
	benchCycle(b, c)
}

func BenchmarkAuthCycleLocalTLS(b *testing.B) {
	// Pinned self-signed fixture: a real TLS handshake + verification
	// per connection, trust via the pin anchor (no system-trust
	// dependency inside benchmarks). CPU cost of TLS vs the HTTP run
	// above is the interesting delta.
	cert, der := selfSignedBenchCert(b)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "bench", Path: "/"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/sync/maindata":
			w.Write([]byte(`{"rid":1,"full_update":true,"torrents":{},"server_state":{"dl_info_speed":0,"up_info_speed":0}}`))
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5.2.3")
		case "/api/v2/app/webapiVersion":
			fmt.Fprint(w, "2.15.1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	c, err := NewConfigurable(ClientConfig{
		BaseURL: ts.URL, Username: "u",
		SecretFetcher: func(context.Context) ([]byte, error) { return []byte("p"), nil },
		TLS:           TLSOptions{Mode: TLSPin, Pin: Fingerprint(der), PinCertPEM: pemBlock},
	})
	if err != nil {
		b.Fatal(err)
	}
	benchCycle(b, c)
}
