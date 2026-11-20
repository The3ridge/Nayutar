// Package budget implements a pure sliding-window eviction rate budget.
package budget

// Limit returns max(1, ceil(totalNodes*percent/100)). totalNodes < 0 is treated as 0.
func Limit(totalNodes, percent int) int {
	if totalNodes <= 0 || percent <= 0 {
		return 1
	}
	return max(1, (totalNodes*percent+99)/100)
}

// Grant prunes entries not newer than now-window and, if fewer than limit remain, records now.
func Grant(granted []int64, now, window int64, limit int) (next []int64, ok bool) {
	kept := make([]int64, 0, len(granted)+1)
	cutoff := now - window
	for _, e := range granted {
		if e > cutoff {
			kept = append(kept, e)
		}
	}
	if len(kept) >= limit {
		return kept, false
	}
	idx := len(kept)
	for i, e := range kept {
		if e > now {
			idx = i
			break
		}
	}
	kept = append(kept, 0)
	copy(kept[idx+1:], kept[idx:])
	kept[idx] = now
	return kept, true
}
