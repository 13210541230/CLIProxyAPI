package accountpool

import "time"

const (
	borrowObservation = time.Minute
	borrowRecovery    = 15 * time.Second
)

// occupancyHistory integrates execution time into a bounded ring, rather than
// sampling only when a request happens to inspect an account. The current
// partial second plus 60 completed seconds fit in 61 buckets.
type occupancyHistory struct {
	since     time.Time
	at        time.Time
	active    int
	pressured bool
	lastBusy  time.Time
	buckets   [61]occupancyBucket
}

type occupancyBucket struct {
	second int64
	area   time.Duration
}

// observeLocked records state changes and advances old occupancy to now. All
// callers hold the engine mutex, including selection and completion.
func (e *Engine) observeLocked(authID string, now time.Time) *occupancyHistory {
	h := e.history[authID]
	if h == nil {
		h = &occupancyHistory{since: now, at: now}
		e.history[authID] = h
	}
	start := h.at
	if cutoff := now.Add(-61 * time.Second); start.Before(cutoff) {
		start = cutoff
	}
	for start.Before(now) {
		second := start.Unix()
		end := time.Unix(second+1, 0)
		if end.After(now) {
			end = now
		}
		bucket := &h.buckets[second%int64(len(h.buckets))]
		if bucket.second != second {
			*bucket = occupancyBucket{second: second}
		}
		bucket.area += end.Sub(start) * time.Duration(h.active)
		start = end
	}
	if h.pressured {
		h.lastBusy = now
	}
	h.at = now
	h.active = e.active[authID]
	limit := e.limitOf(authID)
	h.pressured = e.waiting[authID] > 0 || limit.Max > 0 && h.active >= limit.Max
	if h.pressured {
		h.lastBusy = now
	}
	return h
}

func (h *occupancyHistory) mean(now time.Time) (float64, bool) {
	end := now.Truncate(time.Second)
	start := end.Add(-borrowObservation)
	if h.since.After(start) {
		return 0, false
	}
	var area time.Duration
	for second := start.Unix(); second < end.Unix(); second++ {
		bucket := h.buckets[second%int64(len(h.buckets))]
		if bucket.second == second {
			area += bucket.area
		}
	}
	return float64(area) / float64(borrowObservation), true
}

func (e *Engine) borrowableLocked(authID string, now time.Time) bool {
	h := e.observeLocked(authID, now)
	limit := e.limitOf(authID)
	mean, ready := h.mean(now)
	if !ready || limit.Max <= 1 || e.waiting[authID] != 0 {
		return false
	}
	// One slot is spare at allocation only. Existing sessions keep ordinary
	// admission semantics and must never migrate when pressure rises later.
	if e.active[authID]+len(e.reserved[authID])+1 > limit.Max-1 {
		return false
	}
	if mean > float64(limit.Max)*0.4 || !h.lastBusy.IsZero() && now.Sub(h.lastBusy) < borrowRecovery {
		return false
	}
	if limit.Max15s > 0 && e.windowCountLocked(authID, now)+len(e.reserved[authID]) >= limit.Max15s {
		return false
	}
	return true
}

// poolSelection separates new-session permissions from existing-session
// eligibility. Revoking an exemption does not erase a borrowed binding.
type poolSelection struct {
	primary map[string]struct{}
	borrow  bool
}

func (e *Engine) poolLoadsLocked(loads []candidateLoad, selection *poolSelection, now time.Time) []candidateLoad {
	var primary, available, foreign []candidateLoad
	for _, load := range loads {
		if _, ok := selection.primary[load.AuthID]; ok {
			primary = append(primary, load)
			limit := e.limitOf(load.AuthID)
			reserved := len(e.reserved[load.AuthID])
			if e.waiting[load.AuthID] == 0 &&
				(limit.Max <= 0 || e.active[load.AuthID]+reserved < limit.Max) &&
				(limit.Max15s <= 0 || e.windowCountLocked(load.AuthID, now)+reserved < limit.Max15s) {
				available = append(available, load)
			}
		} else if selection.borrow && e.borrowableLocked(load.AuthID, now) {
			foreign = append(foreign, load)
		}
	}
	if !selection.borrow {
		// Ordinary callers retain the existing primary-pool scoring and queue
		// behavior. Capacity-first allocation belongs only to exempt sessions.
		return primary
	}
	if len(available) > 0 {
		return available
	}
	if len(foreign) > 0 {
		return foreign
	}
	return primary // Retain primary queuing instead of relaxing borrowing gates.
}
