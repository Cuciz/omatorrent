// Dashboard aggregation tests (ADR-0007): classification correctness,
// saturating sums, per-torrent remaining clamp, active-list ordering and
// cap, zero torrents, and degraded-state free-space semantics at the
// syncer level.
package state

import (
	"math"
	"testing"
)

func TestAggregateEmpty(t *testing.T) {
	d := Aggregate(State{Torrents: map[string]Torrent{}})
	if (d.Counts != DashboardCounts{}) {
		t.Fatalf("counts = %+v, want zero", d.Counts)
	}
	if (d.Aggregate != DashboardAggregate{}) {
		t.Fatalf("aggregate = %+v, want zero", d.Aggregate)
	}
	if len(d.Active) != 0 {
		t.Fatalf("active = %d entries, want 0", len(d.Active))
	}
	if d.FreeSpace != nil {
		t.Fatalf("free space = %v, want nil", *d.FreeSpace)
	}
}

func TestAggregateClassification(t *testing.T) {
	st := State{Torrents: map[string]Torrent{
		"a": {Hash: "a", Name: "dl", State: StateDownloading, Progress: 0.5, Size: 100, Completed: 50, DlSpeed: 10},
		"b": {Hash: "b", Name: "seed", State: StateSeeding, Progress: 1, Size: 200, Completed: 200, UpSpeed: 5},
		"c": {Hash: "c", Name: "paused", State: StatePaused, Progress: 0.25, Size: 400, Completed: 0},
		"d": {Hash: "d", Name: "queued", State: StateQueued, Progress: 0.75, Size: 800, Completed: 600},
		"e": {Hash: "e", Name: "done-seed", State: StateSeeding, Progress: 1, Size: 1600, Completed: 1600},
		"f": {Hash: "f", Name: "other", State: StateOther, Progress: 1, Size: 3200, Completed: 3200},
		"g": {Hash: "g", Name: "err", State: StateError, Progress: 0, Size: 6400, Completed: 0},
	}}
	d := Aggregate(st)
	want := DashboardCounts{Total: 7, Active: 2, Downloading: 1, Seeding: 2, Paused: 1, Completed: 3}
	if d.Counts != want {
		t.Fatalf("counts = %+v, want %+v", d.Counts, want)
	}
	// total 100+200+400+800+1600+3200+6400 = 12700; completed 50+200+0+600+1600+3200+0 = 5650
	// remaining per torrent: 50+0+400+200+0+0+6400 = 7050
	wantAgg := DashboardAggregate{TotalSize: 12700, CompletedBytes: 5650, RemainingBytes: 7050}
	if d.Aggregate != wantAgg {
		t.Fatalf("aggregate = %+v, want %+v", d.Aggregate, wantAgg)
	}
}

func TestAggregateRemainingClamp(t *testing.T) {
	// completed > size on one torrent must not make remaining negative.
	st := State{Torrents: map[string]Torrent{
		"a": {Hash: "a", State: StateDownloading, Size: 100, Completed: 250},
	}}
	d := Aggregate(st)
	if d.Aggregate.RemainingBytes != 0 {
		t.Fatalf("remaining = %d, want 0 (clamped)", d.Aggregate.RemainingBytes)
	}
	if d.Aggregate.CompletedBytes != 250 {
		t.Fatalf("completed = %d, want 250", d.Aggregate.CompletedBytes)
	}
}

func TestAggregateSaturatingSums(t *testing.T) {
	st := State{Torrents: map[string]Torrent{
		"a": {Hash: "a", State: StateSeeding, Size: math.MaxInt64, Completed: math.MaxInt64},
		"b": {Hash: "b", State: StateSeeding, Size: math.MaxInt64, Completed: 1},
	}}
	d := Aggregate(st)
	if d.Aggregate.TotalSize != math.MaxInt64 {
		t.Fatalf("total = %d, want saturating MaxInt64", d.Aggregate.TotalSize)
	}
	if d.Aggregate.CompletedBytes != math.MaxInt64 {
		t.Fatalf("completed = %d, want saturating MaxInt64", d.Aggregate.CompletedBytes)
	}
}

func TestAggregateActiveOrderingAndCap(t *testing.T) {
	mk := func(name string, dl, up int64) Torrent {
		return Torrent{Hash: name, Name: name, State: StateDownloading, DlSpeed: dl, UpSpeed: up}
	}
	st := State{Torrents: map[string]Torrent{
		"a": mk("alpha", 10, 0),
		"b": mk("beta", 0, 5),   // 5 combined
		"c": mk("gamma", 50, 0), // 50 combined
		"d": mk("delta", 30, 0),
		"e": mk("eps", 20, 0),
		"f": mk("zeta", 0, 60),
		"g": mk("eta", 0, 0), // not active
		"h": mk("theta", 7, 0),
	}}
	d := Aggregate(st)
	if d.Counts.Active != 7 {
		t.Fatalf("active count = %d, want 7", d.Counts.Active)
	}
	if len(d.Active) != ActiveListCap {
		t.Fatalf("active list len = %d, want %d", len(d.Active), ActiveListCap)
	}
	wantOrder := []string{"zeta", "gamma", "delta", "eps", "alpha"}
	for i, w := range wantOrder {
		if d.Active[i].Name != w {
			t.Fatalf("active[%d] = %s, want %s (full order: %v)", i, d.Active[i].Name, w, names(d.Active))
		}
	}
}

func TestAggregateActiveTieBreakByName(t *testing.T) {
	st := State{Torrents: map[string]Torrent{
		"z": {Hash: "z", Name: "b-second", DlSpeed: 5},
		"a": {Hash: "a", Name: "a-first", UpSpeed: 5},
	}}
	d := Aggregate(st)
	if d.Active[0].Name != "a-first" || d.Active[1].Name != "b-second" {
		t.Fatalf("tie order = %v, want name-ascending", names(d.Active))
	}
}

func TestAggregateFreeSpacePassThrough(t *testing.T) {
	fs := int64(123456789)
	st := State{Torrents: map[string]Torrent{}, FreeSpace: &fs}
	d := Aggregate(st)
	if d.FreeSpace == nil || *d.FreeSpace != fs {
		t.Fatalf("free space not passed through")
	}
}

func names(items []DashboardActiveItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}
