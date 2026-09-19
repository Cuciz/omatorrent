// IPC v1.4 wire shapes: connection management (ADR-0008). Response
// types only — request parsing lives in protocol.go's parseFrame.
// No response ever contains a secret; the test result exposes the
// offered certificate's FINGERPRINT (never the certificate bytes).
package ipc

// ConnectionStatusData is the cache-served status payload the handler
// supplies (all fields non-secret; username is intentionally included
// for the settings form — status surfaces display only the host).
type ConnectionStatusData struct {
	Configured bool
	Mode       string // local | remote
	URL        string // validated origin (non-secret; settings prefill)
	Host       string
	Transport  string // http | https
	Insecure   bool
	Username   string
	HasSecret  bool
	TLSMode    string
	Pin        string // active pinned fingerprint (iff pin mode; non-secret)
	Status     string
	Detail     string
	Epoch      uint64
}

type connectionStatusResponse struct {
	Type       string `json:"type"`
	Protocol   int64  `json:"protocol"`
	ID         int64  `json:"id"`
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
	URL        string `json:"url"`
	Host       string `json:"host"`
	Transport  string `json:"transport"`
	Insecure   bool   `json:"insecure"`
	Username   string `json:"username"`
	HasSecret  bool   `json:"has_secret"`
	TLSMode    string `json:"tls_mode"`
	Pin        string `json:"pin,omitempty"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	Epoch      uint64 `json:"epoch"`
}

// ConnectionTestRequest is a schema-validated connection.test request.
type ConnectionTestRequest struct {
	URL               string
	Username          string
	Password          []byte // nil = not provided (wiped by the server after the handler)
	UseStoredPassword bool
	TLSMode           string
	Pin               string
	AllowInsecureHTTP bool
}

// ConnectionTestData is the normalized test outcome.
type ConnectionTestData struct {
	OK                 bool
	Status             string
	Detail             string
	Host               string
	Transport          string
	AppVersion         string
	WebAPIVersion      string
	OfferedFingerprint string
}

type connectionTestResponse struct {
	Type               string  `json:"type"`
	Protocol           int64   `json:"protocol"`
	ID                 int64   `json:"id"`
	Result             string  `json:"result"` // ok | failed
	Status             string  `json:"status"`
	Host               string  `json:"host"`
	Transport          string  `json:"transport"`
	AppVersion         *string `json:"app_version,omitempty"`
	WebAPIVersion      *string `json:"webapi_version,omitempty"`
	OfferedFingerprint string  `json:"offered_fingerprint,omitempty"`
	Detail             string  `json:"detail,omitempty"`
}

// ConnectionConfigureRequest is a schema-validated configure request.
type ConnectionConfigureRequest struct {
	ConnectionTestRequest
	SecretAction string // keep | replace | delete
}

// ConnectionConfigureData is the activation outcome: OK, or a fixed
// rejection code (docs/IPC.md v1.4).
type ConnectionConfigureData struct {
	OK        bool
	Rejection string
	Epoch     uint64
	Mode      string
	Host      string
	Transport string
}

type connectionConfiguredResponse struct {
	Type      string `json:"type"`
	Protocol  int64  `json:"protocol"`
	ID        int64  `json:"id"`
	Epoch     uint64 `json:"epoch"`
	Mode      string `json:"mode"`
	Host      string `json:"host"`
	Transport string `json:"transport"`
}

type connectionRejectedResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Code     string `json:"code"`
}

// Connections supplies the v1.4 surface. ConnectionStatus is
// cache-served; Test/Configure perform bounded work (Manager budget
// 8 s) and must be safe for concurrent use.
type Connections interface {
	ConnectionStatus() ConnectionStatusData
	ConnectionTest(r ConnectionTestRequest) ConnectionTestData
	ConnectionConfigure(r ConnectionConfigureRequest) ConnectionConfigureData
}

// EncodeConnectionStatus encodes the v1.4 status response. String
// fields are capped so the frame invariant holds against pathological
// providers (same class as the v1.3 caps).
func EncodeConnectionStatus(id int64, d ConnectionStatusData) []byte {
	return mustMarshal(connectionStatusResponse{
		Type: "connection.status", Protocol: ProtocolVersion, ID: id,
		Configured: d.Configured,
		Mode:       capString(d.Mode, 16),
		URL:        capString(d.URL, 256),
		Host:       capString(d.Host, 128),
		Transport:  capString(d.Transport, 8),
		Insecure:   d.Insecure,
		Username:   capString(d.Username, 64),
		HasSecret:  d.HasSecret,
		TLSMode:    capString(d.TLSMode, 16),
		Pin:        capString(d.Pin, 64),
		Status:     capString(d.Status, 32),
		Detail:     capString(d.Detail, 128),
		Epoch:      d.Epoch,
	})
}

// EncodeConnectionTest encodes the v1.4 test response. Version fields
// are present iff ok; the offered fingerprint iff a certificate was
// presented and rejected.
func EncodeConnectionTest(id int64, d ConnectionTestData) []byte {
	result := "failed"
	if d.OK {
		result = "ok"
	}
	resp := connectionTestResponse{
		Type: "connection.test", Protocol: ProtocolVersion, ID: id,
		Result:    result,
		Status:    capString(d.Status, 32),
		Host:      capString(d.Host, 128),
		Transport: capString(d.Transport, 8),
		Detail:    capString(d.Detail, 128),
	}
	if d.OK {
		av, wv := capString(d.AppVersion, 64), capString(d.WebAPIVersion, 64)
		resp.AppVersion, resp.WebAPIVersion = &av, &wv
	}
	if !d.OK && d.OfferedFingerprint != "" {
		resp.OfferedFingerprint = capString(d.OfferedFingerprint, 64)
	}
	return mustMarshal(resp)
}

// EncodeConnectionConfigured encodes the accepted activation.
func EncodeConnectionConfigured(id int64, d ConnectionConfigureData) []byte {
	return mustMarshal(connectionConfiguredResponse{
		"connection.configured", ProtocolVersion, id,
		d.Epoch, capString(d.Mode, 16), capString(d.Host, 128), capString(d.Transport, 8),
	})
}

// EncodeConnectionRejected encodes the refused activation (fixed code,
// no payload echo).
func EncodeConnectionRejected(id int64, code string) []byte {
	return mustMarshal(connectionRejectedResponse{
		"connection.rejected", ProtocolVersion, id, capString(code, 32),
	})
}
