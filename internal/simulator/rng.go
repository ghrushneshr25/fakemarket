package simulator

import "hash/fnv"

// RNG is a deterministic xorshift64* generator (not crypto).
type RNG struct {
	s uint64
}

// NewRNG seeds from a 64-bit value (0 is remapped).
func NewRNG(seed int64) *RNG {
	s := uint64(seed)
	if s == 0 {
		s = 0xDEADBEEFCAFEBABE
	}
	return &RNG{s: s}
}

func (r *RNG) next() uint64 {
	x := r.s
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.s = x
	return x * 0x2545F4914F6CDD1D
}

// Uint64 returns next value.
func (r *RNG) Uint64() uint64 { return r.next() }

// Intn returns [0,n).
func (r *RNG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// Float64 returns [0,1).
func (r *RNG) Float64() float64 {
	return float64(r.next()>>11) / (1 << 53)
}

// Bool returns true with probability p in [0,1].
func (r *RNG) Bool(p float64) bool {
	return r.Float64() < p
}

// Pick returns index in items using rng.
func (r *RNG) Pick(n int) int { return r.Intn(n) }

// HashString is a stable FNV-1a for fingerprints.
func HashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
