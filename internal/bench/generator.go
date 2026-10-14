package bench

import (
	"math/rand"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Generator produces deterministic command shapes from a seed.
// Same (seed, scenario, i) → same logical command parameters.
type Generator struct {
	rng  *rand.Rand
	seed int64
}

// NewGenerator creates a seeded PRNG (not crypto).
func NewGenerator(seed int64) *Generator {
	return &Generator{seed: seed, rng: rand.New(rand.NewSource(seed))}
}

// Seed returns the configured seed.
func (g *Generator) Seed() int64 { return g.seed }

// PriceAround returns a deterministic price for index i near base.
func (g *Generator) PriceAround(base domain.Price, i int, spread int) domain.Price {
	// pure function of i + seed offset for reproducibility without consuming rng state differently per call order
	off := int64(i)*31 + g.seed
	d := int(off%int64(spread*2+1)) - spread
	p := int64(base) + int64(d)
	if p < 1 {
		p = 1
	}
	return domain.Price(p)
}

// SideFor returns BUY/SELL alternating in a seed-stable pattern.
func (g *Generator) SideFor(i int) domain.Side {
	if (int64(i)+g.seed)%2 == 0 {
		return domain.SideBuy
	}
	return domain.SideSell
}

// ShouldCancel returns true for cancel-heavy ratio (e.g. 30% when ratio=30).
func (g *Generator) ShouldCancel(i int, cancelPct int) bool {
	if cancelPct <= 0 {
		return false
	}
	return (int64(i)*17+g.seed)%100 < int64(cancelPct)
}

// MatchMode for mixed workloads.
type MatchMode int

const (
	ModeNoMatch MatchMode = iota
	ModeFullMatch
	ModePartial
	ModeCancel
)

// ModeFor maps i into a stable mixed distribution.
func (g *Generator) ModeFor(i int) MatchMode {
	switch (int64(i) + g.seed) % 4 {
	case 0:
		return ModeNoMatch
	case 1:
		return ModeFullMatch
	case 2:
		return ModePartial
	default:
		return ModeCancel
	}
}
