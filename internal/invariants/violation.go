// Package invariants provides read-only financial invariant verification for FakeMarket (P8.6).
//
// Equations are derived from the existing domain/recovery model — not invented settlement rules.
// The verifier never mutates exchange state and has no transport/chaos dependencies.
package invariants

import (
	"fmt"
	"strings"
)

// Violation is one structured invariant failure (test-friendly, bounded diagnostics).
type Violation struct {
	Name     string // e.g. "order.quantity_conservation"
	Entity   string // order_id / trade_id / account_id / instrument
	Expected string
	Actual   string
	Context  string // sequence, side, extra
}

func (v Violation) Error() string {
	var b strings.Builder
	b.WriteString("invariant:")
	b.WriteString(v.Name)
	if v.Entity != "" {
		b.WriteString(" entity=")
		b.WriteString(v.Entity)
	}
	if v.Expected != "" {
		b.WriteString(" expected=")
		b.WriteString(v.Expected)
	}
	if v.Actual != "" {
		b.WriteString(" actual=")
		b.WriteString(v.Actual)
	}
	if v.Context != "" {
		b.WriteString(" ctx=")
		b.WriteString(v.Context)
	}
	return b.String()
}

// Report aggregates zero or more violations.
type Report struct {
	Violations []Violation
}

func (r *Report) add(v Violation) {
	r.Violations = append(r.Violations, v)
}

func (r *Report) OK() bool { return r == nil || len(r.Violations) == 0 }

func (r *Report) Error() error {
	if r.OK() {
		return nil
	}
	msgs := make([]string, 0, len(r.Violations))
	for _, v := range r.Violations {
		msgs = append(msgs, v.Error())
	}
	// Cap dump size for huge states.
	const max = 20
	if len(msgs) > max {
		msgs = append(msgs[:max], fmt.Sprintf("... and %d more", len(r.Violations)-max))
	}
	return fmt.Errorf("invariant violations (%d):\n  %s", len(r.Violations), strings.Join(msgs, "\n  "))
}
