package budget

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestLimit(t *testing.T) {
	tests := []struct {
		totalNodes int
		percent    int
		want       int
	}{
		{-5, 10, 1},
		{0, 10, 1},
		{1, 10, 1},
		{10, 10, 1},
		{11, 10, 2},
		{100, 30, 30},
		{101, 30, 31},
		{10, 0, 1},
		{10, -5, 1},
	}

	for _, tc := range tests {
		if got := Limit(tc.totalNodes, tc.percent); got != tc.want {
			t.Errorf("Limit(%d, %d) = %d, want %d", tc.totalNodes, tc.percent, got, tc.want)
		}
	}
}

func TestGrantTable(t *testing.T) {
	tests := []struct {
		name     string
		granted  []int64
		now      int64
		window   int64
		limit    int
		wantNext []int64
		wantOK   bool
	}{
		{
			name:     "empty slice granted",
			granted:  nil,
			now:      100,
			window:   60,
			limit:    2,
			wantNext: []int64{100},
			wantOK:   true,
		},
		{
			name:     "exact boundary now-window is pruned",
			granted:  []int64{40, 41},
			now:      100,
			window:   60,
			limit:    2,
			wantNext: []int64{41, 100},
			wantOK:   true,
		},
		{
			name:     "at limit after pruning denies",
			granted:  []int64{40, 41, 90},
			now:      100,
			window:   60,
			limit:    2,
			wantNext: []int64{41, 90},
			wantOK:   false,
		},
		{
			name:     "limit zero denies and prunes",
			granted:  []int64{10, 95},
			now:      100,
			window:   60,
			limit:    0,
			wantNext: []int64{95},
			wantOK:   false,
		},
		{
			name:     "now smaller than existing entries inserts before first strictly greater",
			granted:  []int64{50, 60, 60, 80},
			now:      60,
			window:   100,
			limit:    5,
			wantNext: []int64{50, 60, 60, 60, 80},
			wantOK:   true,
		},
		{
			name:     "now smaller than all kept inserts at front",
			granted:  []int64{50, 60},
			now:      40,
			window:   100,
			limit:    5,
			wantNext: []int64{40, 50, 60},
			wantOK:   true,
		},
		{
			name:     "unsorted input prunes without assuming order and inserts before first > now",
			granted:  []int64{80, 10, 50, 30, 70, 60},
			now:      55,
			window:   70, // cutoff = -15 -> all entries kept
			limit:    10,
			wantNext: []int64{55, 80, 10, 50, 30, 70, 60},
			wantOK:   true,
		},
		{
			name:     "unsorted input with pruning",
			granted:  []int64{80, 10, 50, 30, 70, 60},
			now:      55,
			window:   25, // cutoff = 30 -> 10 and 30 pruned; kept = [80, 50, 70, 60]
			limit:    5,
			wantNext: []int64{55, 80, 50, 70, 60},
			wantOK:   true,
		},
		{
			name:     "unsorted input inserts after equal elements before first strictly greater",
			granted:  []int64{40, 55, 55, 70, 35},
			now:      55,
			window:   30, // cutoff = 25 -> all kept
			limit:    6,
			wantNext: []int64{40, 55, 55, 55, 70, 35},
			wantOK:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Give input extra backing capacity to catch accidental in-place mutation.
			var input []int64
			var snapshot []int64
			if tc.granted != nil {
				backing := make([]int64, len(tc.granted)+4)
				copy(backing, tc.granted)
				backing[len(tc.granted)] = 999999
				input = backing[:len(tc.granted)]
				snapshot = slices.Clone(tc.granted)
			}

			gotNext, gotOK := Grant(input, tc.now, tc.window, tc.limit)
			if gotOK != tc.wantOK || !slices.Equal(gotNext, tc.wantNext) {
				t.Fatalf("Grant(%v, %d, %d, %d) = (%v, %v), want (%v, %v)",
					tc.granted, tc.now, tc.window, tc.limit, gotNext, gotOK, tc.wantNext, tc.wantOK)
			}
			if tc.granted != nil {
				if !slices.Equal(input, snapshot) {
					t.Fatalf("input slice mutated: got %v, want %v", input, snapshot)
				}
				if input[:cap(input)][len(tc.granted)] != 999999 {
					t.Fatalf("spare capacity of input slice mutated")
				}
			}
		})
	}
}

func TestGrantPropertyNonDecreasingNow(t *testing.T) {
	rng := rand.New(rand.NewPCG(0xbad6e7, 0x123456789abcdef0))

	for trial := range 500 {
		window := int64(1 + rng.IntN(500))
		limit := 1 + rng.IntN(10)
		var state []int64
		var allGranted []int64
		now := int64(0)

		for step := range 200 {
			now += int64(rng.IntN(80))
			prevCopy := slices.Clone(state)
			next, ok := Grant(state, now, window, limit)
			if !slices.Equal(state, prevCopy) {
				t.Fatalf("trial %d step %d: input slice mutated", trial, step)
			}
			if !slices.IsSorted(next) {
				t.Fatalf("trial %d step %d: next slice %v not sorted", trial, step, next)
			}
			if ok {
				allGranted = append(allGranted, now)
				if len(next) > limit {
					t.Fatalf("trial %d step %d: len(next)=%d > limit=%d", trial, step, len(next), limit)
				}
			} else if len(next) < limit {
				t.Fatalf("trial %d step %d: denied when len(next)=%d < limit=%d", trial, step, len(next), limit)
			}
			state = next

			// Check every window (t-window, t] across history and current time.
			for _, tVal := range allGranted {
				count := 0
				for _, g := range allGranted {
					if g > tVal-window && g <= tVal {
						count++
					}
				}
				if count > limit {
					t.Fatalf("trial %d step %d: window (%d, %d] has %d > limit %d",
						trial, step, tVal-window, tVal, count, limit)
				}
			}
			countNow := 0
			for _, g := range allGranted {
				if g > now-window && g <= now {
					countNow++
				}
			}
			if countNow > limit || countNow != len(state) {
				t.Fatalf("trial %d step %d: active window (%d, %d] count=%d len(state)=%d limit=%d",
					trial, step, now-window, now, countNow, len(state), limit)
			}
		}
	}
}
