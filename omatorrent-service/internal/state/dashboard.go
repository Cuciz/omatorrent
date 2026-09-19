// Dashboard aggregation (Phase 0.4, ADR-0007): a pure function over the
// committed state that produces every number the dashboard displays.
// Classification semantics deliberately match the panel's filters
// (Phase 0.2/0.3) so the two surfaces can never disagree. Current state
// only — no history, no persistence.
package state

import (
	"math"
	"sort"
)

// DashboardCounts is the torrent population summary.
type DashboardCounts struct {
	Total       int
	Active      int
	Downloading int
	Seeding     int
	Paused      int
	Completed   int
}

// DashboardAggregate is the current data summary in bytes. Sums are
// saturating at MaxInt64 so a hostile/buggy backend value can never wrap
// to a negative total.
type DashboardAggregate struct {
	TotalSize      int64
	CompletedBytes int64
	RemainingBytes int64
}

// DashboardActiveItem is one entry of the "transferring now" list.
type DashboardActiveItem struct {
	Name     string
	State    string
	Progress float64
	DlSpeed  int64
	UpSpeed  int64
}

// Dashboard is the v1.3 response payload (ADR-0007).
type Dashboard struct {
	Counts    DashboardCounts
	Aggregate DashboardAggregate
	Active    []DashboardActiveItem
	FreeSpace *int64
}

// ActiveListCap bounds the transferring-now list (ADR-0007: up to 5).
const ActiveListCap = 5

func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// Aggregate computes the dashboard payload from one committed state.
// O(N) over torrents with a bounded sort of the active candidates;
// deterministic ordering (speed desc, then name asc, then hash asc).
func Aggregate(st State) Dashboard {
	d := Dashboard{FreeSpace: st.FreeSpace}
	type cand struct {
		it    DashboardActiveItem
		hash  string
		speed int64
	}
	var cands []cand
	for h, t := range st.Torrents {
		d.Counts.Total++
		switch t.State {
		case StateDownloading:
			d.Counts.Downloading++
		case StateSeeding:
			d.Counts.Seeding++
		case StatePaused:
			d.Counts.Paused++
		}
		if t.Progress >= 1 {
			d.Counts.Completed++
		}
		d.Aggregate.TotalSize = satAdd(d.Aggregate.TotalSize, t.Size)
		d.Aggregate.CompletedBytes = satAdd(d.Aggregate.CompletedBytes, t.Completed)
		// Per-torrent clamp before summing: completed > size on one
		// torrent must never make the total remaining negative. Compare
		// first, then saturating-subtract — a raw Size-Completed can
		// wrap positive when backend values are extreme (review finding).
		if t.Size > t.Completed {
			d.Aggregate.RemainingBytes = satAdd(d.Aggregate.RemainingBytes, satAdd(t.Size, -t.Completed))
		}
		if t.DlSpeed > 0 || t.UpSpeed > 0 {
			d.Counts.Active++
			cands = append(cands, cand{
				it: DashboardActiveItem{
					Name:     t.Name,
					State:    t.State,
					Progress: t.Progress,
					DlSpeed:  t.DlSpeed,
					UpSpeed:  t.UpSpeed,
				},
				hash:  h,
				speed: satAdd(t.DlSpeed, t.UpSpeed),
			})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].speed != cands[j].speed {
			return cands[i].speed > cands[j].speed
		}
		if cands[i].it.Name != cands[j].it.Name {
			return cands[i].it.Name < cands[j].it.Name
		}
		return cands[i].hash < cands[j].hash
	})
	if len(cands) > ActiveListCap {
		cands = cands[:ActiveListCap]
	}
	for _, c := range cands {
		d.Active = append(d.Active, c.it)
	}
	return d
}
