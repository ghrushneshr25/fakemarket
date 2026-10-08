package matching

import "time"

// MutableClock is a deterministic clock controlled by the execution/replay engine.
type MutableClock struct {
	t time.Time
}

// NewMutableClock starts at the given UTC time (or Unix 0 if zero).
func NewMutableClock(t time.Time) *MutableClock {
	if t.IsZero() {
		t = time.Unix(0, 0).UTC()
	}
	return &MutableClock{t: t.UTC()}
}

func (c *MutableClock) NowUTC() time.Time { return c.t }

// Set advances the clock to t (normalized UTC).
func (c *MutableClock) Set(t time.Time) {
	if t.IsZero() {
		c.t = time.Unix(0, 0).UTC()
		return
	}
	c.t = t.UTC()
}
