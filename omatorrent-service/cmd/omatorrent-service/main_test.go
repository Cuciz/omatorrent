package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/ipc"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

// benchBackend serves one full-update maindata with n torrents, all
// transferring (the active-list worst case).
type benchBackend struct{ n int }

func (b *benchBackend) Login(ctx context.Context) error                { return nil }
func (b *benchBackend) AppVersion(ctx context.Context) (string, error) { return "v5.2.3", nil }
func (b *benchBackend) WebAPIVersion(ctx context.Context) (string, error) {
	return "2.15.1", nil
}

func (b *benchBackend) SyncMaindata(ctx context.Context, rid int64) (qbittorrent.Maindata, error) {
	torrents := make(map[string]json.RawMessage, b.n)
	for i := 0; i < b.n; i++ {
		hash := fmt.Sprintf("%040x", i)
		name := fmt.Sprintf("Bench torrent %d with a moderately long name", i)
		torrents[hash] = json.RawMessage(`{"name":"` + name + `","state":"downloading","progress":0.42,"dlspeed":123456,"upspeed":65432,"eta":3600,"ratio":1.37,"category":"cat","size":1073741824,"completed":450971566}`)
	}
	return qbittorrent.Maindata{
		RID: 1, FullUpdate: true, Torrents: torrents,
		ServerState: &qbittorrent.ServerState{
			DlInfoSpeed: ptrBench(1), UpInfoSpeed: ptrBench(2),
			ConnectionStatus: ptrBenchS("connected"), FreeSpaceOnDisk: ptrBench(50210201600),
		},
	}, nil
}

func ptrBench(v int64) *int64    { return &v }
func ptrBenchS(v string) *string { return &v }

// startSynced runs the syncer's loop until the first cycle commits the
// fixture, then stops the loop (state stays committed).
func startSynced(b *testing.B, n int) *state.Syncer {
	b.Helper()
	syncer := state.New(&benchBackend{n: n}, state.Options{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go syncer.Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := syncer.State(); st.BackendOK && len(st.Torrents) == n {
			cancel()
			return syncer
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	b.Fatal("syncer did not commit the fixture in time")
	return nil
}

// BenchmarkDashboardStatusResponse measures the FULL per-request
// dashboard.status construction path — everything the daemon does per
// poll except socket I/O: syncer.State() snapshot (RLock + map clone)
// + state.Aggregate + the daemonHandler field adaptation +
// EncodeDashboardStatus. Layered deliberately above the state-layer
// benchmarks (aggregate-only; state+aggregate) so each cost component
// stays attributable.
func BenchmarkDashboardStatusResponse(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			h := &daemonHandler{syncer: startSynced(b, n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, ok := h.Dashboard()
				out := ipc.EncodeDashboardStatus(1, ok, d)
				if len(out) == 0 {
					b.Fatal("empty frame")
				}
			}
		})
	}
}
