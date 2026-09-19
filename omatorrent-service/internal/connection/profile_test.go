package connection

import "testing"

func TestValidateURLAccepts(t *testing.T) {
	cases := []struct {
		raw, wantURL, wantHost string
		loopback               bool
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:8080", true},
		{"http://localhost:8080/", "http://localhost:8080", "localhost:8080", true},
		{"http://[::1]:8080", "http://[::1]:8080", "[::1]:8080", true},
		{"http://127.0.0.1", "http://127.0.0.1:80", "127.0.0.1", true},
		{"https://qbittorrent.home.arpa", "https://qbittorrent.home.arpa:443", "qbittorrent.home.arpa", false},
		{"https://qbittorrent.home.arpa:8443/", "https://qbittorrent.home.arpa:8443", "qbittorrent.home.arpa:8443", false},
		// Reverse-proxy sub-path (prefix stripped at the proxy,
		// docs/QBITTORRENT.md): must NOT collapse into the origin.
		{"https://server.example/qbt", "https://server.example:443/qbt", "server.example/qbt", false},
		{"https://server.example/qbt/", "https://server.example:443/qbt", "server.example/qbt", false},
		{"http://192.168.1.40:8080", "http://192.168.1.40:8080", "192.168.1.40:8080", false},
	}
	for _, c := range cases {
		ep, err := ValidateURL(c.raw)
		if err != nil {
			t.Errorf("ValidateURL(%q): %v", c.raw, err)
			continue
		}
		if ep.NormalizedURL != c.wantURL {
			t.Errorf("ValidateURL(%q).NormalizedURL = %q, want %q", c.raw, ep.NormalizedURL, c.wantURL)
		}
		if ep.Host != c.wantHost {
			t.Errorf("ValidateURL(%q).Host = %q, want %q", c.raw, ep.Host, c.wantHost)
		}
		if ep.IsLoopback != c.loopback {
			t.Errorf("ValidateURL(%q).IsLoopback = %v", c.raw, ep.IsLoopback)
		}
	}
}

// The sub-path join rule (task §8): a /qbt base must never produce
// origin-rooted API paths.
func TestSubPathPrefixSurvives(t *testing.T) {
	ep, err := ValidateURL("https://server.example/qbt/")
	if err != nil {
		t.Fatal(err)
	}
	if ep.Path != "/qbt" {
		t.Fatalf("path = %q", ep.Path)
	}
	joined := ep.NormalizedURL + "/api/v2/app/version"
	want := "https://server.example:443/qbt/api/v2/app/version"
	if joined != want {
		t.Fatalf("join = %q, want %q", joined, want)
	}
}

func TestValidateURLRejects(t *testing.T) {
	bad := []string{
		"",                                   // empty
		"ftp://127.0.0.1:8080",               // scheme
		"file:///etc/passwd",                 // scheme
		"ssh://user@host",                    // scheme + userinfo
		"unix:///run/sock",                   // scheme
		"javascript:alert(1)",                // scheme
		"data:text/plain,hi",                 // scheme
		"gopher://host",                      // scheme
		"https://user:pass@host",             // embedded credentials
		"https://user@host",                  // embedded user
		"https://",                           // empty host
		"https://host:notaport",              // bad port
		"https://host:0",                     // port 0
		"https://host:99999",                 // port range
		"https://host/?q=1",                  // query
		"https://host/#frag",                 // fragment
		"https://host/a/../b",                // dot segment
		"https://host/./a",                   // dot segment
		"https://host//a",                    // empty segment
		"https://host/a\\b",                  // backslash
		"https://ho st/",                     // space
		"https://host/\x01",                  // control char
		"https://host/" + string(make([]byte, 200)), // path too long
		"http://" + string(make([]byte, 2048)),      // total too long
	}
	for _, raw := range bad {
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) accepted", raw)
		}
	}
}

func TestProfileValidate(t *testing.T) {
	// System default normalizes.
	p := Profile{URL: "http://127.0.0.1:8080"}
	if _, err := p.Validate(); err != nil {
		t.Fatalf("default profile: %v", err)
	}
	// ca mode requires a path.
	if _, err := (Profile{URL: "https://h", TLSMode: TLSCA}).Validate(); err == nil {
		t.Error("ca without path accepted")
	}
	// pin mode requires fingerprint + PEM.
	if _, err := (Profile{URL: "https://h", TLSMode: TLSPin}).Validate(); err == nil {
		t.Error("pin without fingerprint accepted")
	}
	if _, err := (Profile{URL: "https://h", TLSMode: TLSPin, PinCertPEM: "x",
		PinFingerprint: "ZZZ"}).Validate(); err == nil {
		t.Error("non-hex fingerprint accepted")
	}
	// Unknown mode.
	if _, err := (Profile{URL: "https://h", TLSMode: "off"}).Validate(); err == nil {
		t.Error("tls mode 'off' accepted — there is no verification-off mode")
	}
	// Username bound.
	if _, err := (Profile{URL: "https://h", Username: string(make([]byte, 128))}).Validate(); err == nil {
		t.Error("oversized username accepted")
	}
}
