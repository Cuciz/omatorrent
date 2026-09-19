package ipc

import (
	"fmt"
	"strings"
	"testing"
)

func TestIPCMetrics(t *testing.T) {
	for _, n := range []int{10, 100, 1000} {
		items := manyItems(n)
		var frames []string
		var largest int
		for i, it := range items {
			f := EncodeSnapshotItem(1, i, it)
			if f == nil {
				t.Fatalf("n=%d item %d unframmable", n, i)
			}
			if len(f) > largest {
				largest = len(f)
			}
		}
		// Representative delta: 10 changed at n=1000, plus a 50-change one.
		d10 := DeltaEvent{Seq: 1, Changed: items[:10]}
		f10, ok := EncodeDeltas(d10)
		if !ok {
			t.Fatal("delta10 failed")
		}
		var d50frames int
		if n >= 50 {
			d50 := DeltaEvent{Seq: 2, Changed: items[:50], Removed: []string{fmt.Sprintf("%040x", 3), fmt.Sprintf("%040x", 4)}}
			f50, ok := EncodeDeltas(d50)
			if !ok {
				t.Fatal("delta50 failed")
			}
			d50frames = len(f50)
		}
		_ = frames
		t.Logf("METRIC n=%d snapshot_frames=%d largest_item_frame=%dB delta10_frames=%d delta50+2rm_frames=%d",
			n, n+3, largest, len(f10), d50frames)
	}
	_ = strings.TrimSpace
}
