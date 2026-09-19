package ipc

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---- v1.3 dashboard aggregates (ADR-0007) ----

// osReadFixture loads one contract example from contracts/ipc/v1.
func osReadFixture(name string) ([]byte, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "contracts", "ipc", "v1")
	return os.ReadFile(filepath.Join(dir, name))
}

func sampleDash() DashboardData {
	fs := int64(50210201600)
	return DashboardData{
		AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
		DlSpeed: 1048576, UpSpeed: 131072,
		FreeSpace: &fs,
		Counts:    DashboardCounts{Total: 3, Active: 1, Downloading: 1, Seeding: 1, Paused: 1, Completed: 2},
		Aggregate: DashboardAggregate{
			TotalSize: 3221225472, CompletedBytes: 2147483648, RemainingBytes: 1073741824,
		},
		Active: []DashboardActiveItem{
			{Name: "A live torrent with a long descriptive name", State: "downloading", Progress: 0.5, DlSpeed: 1048576, UpSpeed: 4096},
		},
	}
}

func TestDashboardStatusRequestGrammar(t *testing.T) {
	// Exact two keys, like health/system.status.
	if _, err := parseFrame([]byte(`{"type":"dashboard.status","id":9}`)); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, bad := range []string{
		`{"type":"dashboard.status"}`,                    // no id
		`{"type":"dashboard.status","id":9,"ref":"r-1"}`, // smuggled v1.2 field
		`{"type":"dashboard.status","id":9,"hash":"0123456789abcdef0123456789abcdef01234567"}`,
		`{"type":"dashboard.status","id":"9"}`, // string id
	} {
		if _, err := parseFrame([]byte(bad)); err == nil {
			t.Fatalf("invalid request accepted: %s", bad)
		}
	}
}

func TestDashboardStatusOKShape(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dial(t, path)
	c.handshake()

	h.setDash(true, sampleDash())
	c.send(`{"type":"dashboard.status","id":9}`)
	want := `{"type":"dashboard.status","protocol":1,"id":9,"qbittorrent":"ok","app_version":"v5.2.3","webapi_version":"2.15.1","dl_speed":1048576,"up_speed":131072,"free_space":50210201600,"counts":{"total":3,"active":1,"downloading":1,"seeding":1,"paused":1,"completed":2},"aggregate":{"total_size":3221225472,"completed_bytes":2147483648,"remaining_bytes":1073741824},"active":[{"name":"A live torrent with a long descriptive name","state":"downloading","progress":0.5,"dlspeed":1048576,"upspeed":4096}]}`
	if got := c.recv(); got != want {
		t.Fatalf("ok shape:\n got %s\nwant %s", got, want)
	}

	// Unknown free space: key omitted, everything else present.
	d := sampleDash()
	d.FreeSpace = nil
	h.setDash(true, d)
	c.send(`{"type":"dashboard.status","id":10}`)
	got := c.recv()
	if strings.Contains(got, "free_space") {
		t.Fatalf("free_space must be omitted when unknown: %s", got)
	}
	// Zero free space is a real value and MUST be present.
	zero := int64(0)
	d2 := sampleDash()
	d2.FreeSpace = &zero
	h.setDash(true, d2)
	c.send(`{"type":"dashboard.status","id":11}`)
	got = c.recv()
	if !strings.Contains(got, `"free_space":0`) {
		t.Fatalf("free_space 0 must be encoded: %s", got)
	}
	// Empty active list: JSON array, never null.
	d3 := sampleDash()
	d3.Active = nil
	h.setDash(true, d3)
	c.send(`{"type":"dashboard.status","id":12}`)
	got = c.recv()
	if !strings.Contains(got, `"active":[]`) {
		t.Fatalf("active must be an empty array: %s", got)
	}
}

func TestDashboardStatusDegradedShapes(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dial(t, path)
	c.handshake()

	// Degraded WITH last-known state.
	d := sampleDash()
	d.LastKnown = &DashboardLastKnownData{
		AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
		Counts:    DashboardCounts{Total: 3, Seeding: 1, Paused: 2, Completed: 1},
		Aggregate: DashboardAggregate{TotalSize: 3221225472, CompletedBytes: 1073741824, RemainingBytes: 2147483648},
	}
	h.setDash(false, d)
	c.send(`{"type":"dashboard.status","id":9}`)
	want := `{"type":"dashboard.status","protocol":1,"id":9,"qbittorrent":"unavailable","last_known":{"app_version":"v5.2.3","webapi_version":"2.15.1","counts":{"total":3,"active":0,"downloading":0,"seeding":1,"paused":2,"completed":1},"aggregate":{"total_size":3221225472,"completed_bytes":1073741824,"remaining_bytes":2147483648}}}`
	if got := c.recv(); got != want {
		t.Fatalf("degraded last-known shape:\n got %s\nwant %s", got, want)
	}

	// Never synced: bare degraded shape, no fabricated zeros.
	h.setDash(false, DashboardData{})
	c.send(`{"type":"dashboard.status","id":10}`)
	want = `{"type":"dashboard.status","protocol":1,"id":10,"qbittorrent":"unavailable"}`
	if got := c.recv(); got != want {
		t.Fatalf("never-synced shape:\n got %s\nwant %s", got, want)
	}
}

func TestDashboardStatusFrameBudget(t *testing.T) {
	// Pathological names: every active entry max-length CONTROL runes —
	// the true amplification worst case (encoding/json \uXXXX-escapes
	// control characters at 6 bytes each but passes CJK through as
	// UTF-8; review finding: the original CJK premise understated this).
	// The encoder must keep the frame within budget.
	long := strings.Repeat("\x01", 512)
	d := DashboardData{
		AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
		DlSpeed: math.MaxInt64, UpSpeed: math.MaxInt64,
		Counts: DashboardCounts{Total: math.MaxInt32, Active: math.MaxInt32, Downloading: math.MaxInt32, Seeding: math.MaxInt32, Paused: math.MaxInt32, Completed: math.MaxInt32},
		Aggregate: DashboardAggregate{
			TotalSize: math.MaxInt64, CompletedBytes: math.MaxInt64, RemainingBytes: math.MaxInt64,
		},
		Active: make([]DashboardActiveItem, 5),
	}
	for i := range d.Active {
		d.Active[i] = DashboardActiveItem{Name: long, State: "downloading", Progress: 0.12345678901234, DlSpeed: math.MaxInt64, UpSpeed: math.MaxInt64}
	}
	b := EncodeDashboardStatus(99, true, d)
	if len(b)+1 > MaxFrame {
		t.Fatalf("frame %d bytes exceeds budget %d", len(b)+1, MaxFrame)
	}
	var resp struct {
		Active []struct {
			Name string `json:"name"`
		} `json:"active"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("budgeted frame not valid JSON: %v", err)
	}
	// Halving guard then entry drops: entries may be fewer than 5, names
	// may be truncated, but the frame stays truthful and parseable.
	t.Logf("worst-case frame: %d bytes, %d active entries", len(b), len(resp.Active))
}

func TestDashboardStatusBudgetDropPath(t *testing.T) {
	// Pins the encoder against the review-found crash class: a caller
	// handing the encoder more than ActiveListWireCap entries with
	// amplifying names must get a bounded, valid frame — never a panic
	// (the refill loop once indexed past a truncated slice). With every
	// string capped, the halving/drop guards are structurally
	// unreachable defense in depth; this drives the entry-cap truncation
	// that precedes them.
	d := DashboardData{
		AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
		Counts:    DashboardCounts{Total: 100, Active: 100},
		Aggregate: DashboardAggregate{TotalSize: 1, CompletedBytes: 1, RemainingBytes: 0},
		Active:    make([]DashboardActiveItem, 100),
	}
	for i := range d.Active {
		d.Active[i] = DashboardActiveItem{
			Name: strings.Repeat("\x01", 256), State: "downloading",
			Progress: 0.5, DlSpeed: 1, UpSpeed: 1,
		}
	}
	b := EncodeDashboardStatus(1, true, d) // must not panic
	if len(b)+1 > MaxFrame {
		t.Fatalf("drop-path frame %d bytes exceeds budget %d", len(b)+1, MaxFrame)
	}
	var resp struct {
		Active []json.RawMessage `json:"active"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("drop-path frame not valid JSON: %v", err)
	}
	if len(resp.Active) > ActiveListWireCap {
		t.Fatalf("encoder emitted %d entries, cap %d", len(resp.Active), ActiveListWireCap)
	}
}

func TestVersionStringsCappedOnWire(t *testing.T) {
	// A hostile/buggy backend version string must never push a frame
	// past the budget: the ENCODER caps it (the adapter caps at its
	// layer too; this pins the wire invariant independently).
	huge := strings.Repeat("v", 4096)
	b := EncodeDashboardStatus(1, true, DashboardData{
		AppVersion: huge, WebAPIVersion: huge,
		Counts: DashboardCounts{Total: math.MaxInt32, Active: math.MaxInt32},
	})
	if len(b)+1 > MaxFrame {
		t.Fatalf("v1.3 ok frame %d bytes exceeds budget with huge versions", len(b)+1)
	}
	b = EncodeDashboardStatus(1, false, DashboardData{
		LastKnown: &DashboardLastKnownData{AppVersion: huge, WebAPIVersion: huge,
			Counts: DashboardCounts{Total: math.MaxInt32}},
	})
	if len(b)+1 > MaxFrame {
		t.Fatalf("v1.3 degraded frame %d bytes exceeds budget with huge versions", len(b)+1)
	}
	b = EncodeStatus(1, true, StatusData{AppVersion: huge, WebAPIVersion: huge,
		DlSpeed: math.MaxInt64, UpSpeed: math.MaxInt64, TorrentsTotal: math.MaxInt32})
	if len(b)+1 > MaxFrame {
		t.Fatalf("v1.0 status frame %d bytes exceeds budget with huge versions", len(b)+1)
	}
}

func TestDashboardStatusAvailableAlongsideSubscription(t *testing.T) {
	// v1.1 rule: v1.0 requests stay available while subscribed; same
	// must hold for v1.3 (docs/IPC.md).
	_, path := startSubServer(t, &fakeSubs{})
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":1}`)
	if got := c.recv(); got != `{"type":"torrent.subscribed","protocol":1,"id":1}` {
		t.Fatalf("subscribed = %s", got)
	}
	// Skip the snapshot frames.
	for {
		line := c.recv()
		var f struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &f) != nil {
			t.Fatalf("bad snapshot frame: %s", line)
		}
		if f.Type == "torrent.snapshot.end" {
			break
		}
	}
	c.send(`{"type":"dashboard.status","id":2}`)
	got := c.recv()
	if !strings.HasPrefix(got, `{"type":"dashboard.status","protocol":1,"id":2,"qbittorrent":"ok"`) {
		t.Fatalf("dashboard.status while subscribed = %s", got)
	}
}

func TestDashboardContractFixtures(t *testing.T) {
	// The request fixture parses; the response fixtures are
	// byte-identical to the encoder output for the same data.
	if _, err := parseFrame([]byte(readFixture(t, "dashboard-status.txt"))); err != nil {
		t.Fatalf("request fixture invalid: %v", err)
	}
	if got := string(EncodeDashboardStatus(9, true, sampleDash())); got != readFixture(t, "response-dashboard-status.txt") {
		t.Fatalf("ok fixture drifted:\n got %s\nwant %s", got, readFixture(t, "response-dashboard-status.txt"))
	}
	d := sampleDash()
	d.LastKnown = &DashboardLastKnownData{
		AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
		Counts:    DashboardCounts{Total: 3, Seeding: 1, Paused: 2, Completed: 1},
		Aggregate: DashboardAggregate{TotalSize: 3221225472, CompletedBytes: 1073741824, RemainingBytes: 2147483648},
	}
	if got := string(EncodeDashboardStatus(9, false, d)); got != readFixture(t, "response-dashboard-status-degraded.txt") {
		t.Fatalf("degraded fixture drifted:\n got %s\nwant %s", got, readFixture(t, "response-dashboard-status-degraded.txt"))
	}
	if got := string(EncodeDashboardStatus(9, false, DashboardData{})); got != readFixture(t, "response-dashboard-status-never-synced.txt") {
		t.Fatalf("never-synced fixture drifted:\n got %s\nwant %s", got, readFixture(t, "response-dashboard-status-never-synced.txt"))
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := osReadFixture(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func TestDashboardFrameSizeIndependentOfPopulation(t *testing.T) {
	// The v1.3 ok frame is O(1) in torrent count: aggregates collapse to
	// fixed-width numbers and the active list is capped at 5. This is
	// the design win over shipping a v1.1 snapshot for the dashboard.
	sizes := map[int]int{}
	for _, n := range []int{10, 100, 1000} {
		d := DashboardData{
			AppVersion: "v5.2.3", WebAPIVersion: "2.15.1",
			DlSpeed: 1048576, UpSpeed: 131072,
			Counts:    DashboardCounts{Total: n, Active: 5, Downloading: n / 2, Seeding: n / 4, Paused: n / 8, Completed: n / 2},
			Aggregate: DashboardAggregate{TotalSize: int64(n) * 1073741824, CompletedBytes: int64(n) * 536870912, RemainingBytes: int64(n) * 536870912},
			Active: []DashboardActiveItem{
				{Name: "Active torrent one", State: "downloading", Progress: 0.5, DlSpeed: 1048576, UpSpeed: 4096},
				{Name: "Active torrent two", State: "downloading", Progress: 0.3, DlSpeed: 524288, UpSpeed: 1024},
				{Name: "Active torrent three", State: "seeding", Progress: 1, DlSpeed: 0, UpSpeed: 2048},
				{Name: "Active torrent four", State: "downloading", Progress: 0.9, DlSpeed: 131072, UpSpeed: 0},
				{Name: "Active torrent five", State: "downloading", Progress: 0.1, DlSpeed: 65536, UpSpeed: 512},
			},
		}
		b := EncodeDashboardStatus(1, true, d)
		if len(b)+1 > MaxFrame {
			t.Fatalf("N=%d frame %d exceeds budget", n, len(b)+1)
		}
		sizes[n] = len(b)
	}
	// Constant except for the digit width of the count values themselves.
	if sizes[1000]-sizes[10] > 16 {
		t.Fatalf("frame size grows with population: %v", sizes)
	}
	t.Logf("frame size across 10/100/1000 torrents: %v bytes (digit-width deltas only)", sizes)
}
