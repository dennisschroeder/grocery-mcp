package browserbridge

import "time"

// TabBinding replaces the outcome-B Credential: there is no secret to hold,
// only proof that a bound tab answered an operation recently. revision bumps
// on each Touch and stands in for SameRevision's old rotation-proof role;
// boundAt doubles as the last-touch time that Expired measures idle against.
type TabBinding struct {
	boundAt  time.Time
	revision uint64
}

func NewTabBinding(now time.Time) *TabBinding {
	return &TabBinding{boundAt: now, revision: 1}
}

func (b *TabBinding) Touch(now time.Time) {
	if b == nil {
		return
	}
	b.boundAt = now
	b.revision++
}

func (b *TabBinding) Expired(now time.Time, maxIdle time.Duration) bool {
	if b == nil || b.boundAt.IsZero() {
		return true
	}
	return now.Sub(b.boundAt) > maxIdle
}

func (b *TabBinding) Clear() {
	if b == nil {
		return
	}
	b.boundAt = time.Time{}
	b.revision = 0
}
