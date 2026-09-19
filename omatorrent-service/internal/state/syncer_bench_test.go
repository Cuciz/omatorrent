package state

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
)

// benchFull builds a full-update payload with n torrents.
func benchFull(n int, state string) qbittorrent.Maindata {
	torrents := make(map[string]json.RawMessage, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("A very long torrent name number %d with season and episode and quality tags", i)
		torrents[mkHash(i)] = json.RawMessage(`{"name":"` + name + `","state":"` + state + `","progress":0.42,"dlspeed":123456,"upspeed":65432,"eta":3600,"ratio":1.37,"category":"cat","size":1073741824,"completed":450971566}`)
	}
	return qbittorrent.Maindata{RID: 1, FullUpdate: true, Torrents: torrents,
		ServerState: &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(1), UpInfoSpeed: ptrInt64(2), ConnectionStatus: ptrString("connected")}}
}

// benchDelta builds a delta touching k of n existing torrents.
func benchDelta(rid int64, n, k int) qbittorrent.Maindata {
	torrents := make(map[string]json.RawMessage, k)
	for i := 0; i < k; i++ {
		torrents[mkHash((i*97)%n)] = json.RawMessage(`{"dlspeed":999999,"upspeed":1,"progress":0.5}`)
	}
	return qbittorrent.Maindata{RID: rid, Torrents: torrents,
		ServerState: &qbittorrent.ServerState{DlInfoSpeed: ptrInt64(9), UpInfoSpeed: ptrInt64(9), ConnectionStatus: ptrString("connected")}}
}

func benchSizes() []int { return []int{10, 100, 1000} }

// BenchmarkSyncFullUpdate measures a full rebuild cycle (the worst case:
// backend restart, or a sessionless backend).
func BenchmarkSyncFullUpdate(b *testing.B) {
	for _, n := range benchSizes() {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			fb := &fakeBackend{}
			s := New(fb, Options{}, quietLogger())
			payload := benchFull(n, "downloading")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fb.mu.Lock()
				fb.resps = []qbittorrent.Maindata{payload}
				fb.mu.Unlock()
				s.rid = 0
				if !s.cycle(context.Background(), time.Second) {
					b.Fatal("cycle failed")
				}
			}
		})
	}
}

// BenchmarkSyncDelta measures the steady-state cost of a small delta
// (10 changed torrents) against a large existing state.
func BenchmarkSyncDelta(b *testing.B) {
	for _, n := range benchSizes() {
		b.Run(fmt.Sprintf("torrents=%d,delta=10", n), func(b *testing.B) {
			fb := &fakeBackend{}
			s := New(fb, Options{}, quietLogger())
			fb.resps = []qbittorrent.Maindata{benchFull(n, "downloading")}
			s.cycle(context.Background(), time.Second)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fb.mu.Lock()
				fb.resps = []qbittorrent.Maindata{benchDelta(int64(i+2), n, 10)}
				fb.mu.Unlock()
				if !s.cycle(context.Background(), time.Second) {
					b.Fatal("cycle failed")
				}
			}
		})
	}
}

// BenchmarkStateRead measures IPC-side snapshot reads (map clone) at
// scale — the cost every system.status/subscribe pays.
func BenchmarkStateRead(b *testing.B) {
	for _, n := range benchSizes() {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			fb := &fakeBackend{resps: []qbittorrent.Maindata{benchFull(n, "seeding")}}
			s := New(fb, Options{}, quietLogger())
			s.cycle(context.Background(), time.Second)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s.State()
			}
		})
	}
}
