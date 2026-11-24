package sim

import (
	"encoding/binary"
	"math/rand/v2"
	"sync"
)

type detRand struct {
	mu  sync.Mutex
	src *rand.ChaCha8
}

// NewDetRand returns a deterministic Rand seeded with seed on stream 0.
func NewDetRand(seed uint64) Rand {
	return NewDetRandStream(seed, 0)
}

// NewDetRandStream returns a deterministic Rand derived from (seed, stream) backed by ChaCha8.
func NewDetRandStream(seed, stream uint64) Rand {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[0:8], seed)
	binary.LittleEndian.PutUint64(key[8:16], stream)
	binary.LittleEndian.PutUint64(key[16:24], seed^0x9e3779b97f4a7c15)
	binary.LittleEndian.PutUint64(key[24:32], stream^0xbf58476d1ce4e5b9)
	return &detRand{src: rand.NewChaCha8(key)}
}

func (r *detRand) Uint64() uint64 {
	r.mu.Lock()
	v := r.src.Uint64()
	r.mu.Unlock()
	return v
}

func (r *detRand) Int64N(n int64) int64 {
	if n <= 0 {
		panic("sim: Int64N argument must be > 0")
	}
	un := uint64(n)
	limit := ^uint64(0) - (^uint64(0) % un)
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		v := r.src.Uint64()
		if v < limit {
			return int64(v % un)
		}
	}
}

// Fill populates p strictly by unpacking little-endian Uint64 words so mixing
// Uint64, Int64N, and Fill never perturbs ChaCha8's internal readBuf state.
func (r *detRand) Fill(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var word [8]byte
	for len(p) >= 8 {
		binary.LittleEndian.PutUint64(p[:8], r.src.Uint64())
		p = p[8:]
	}
	if len(p) > 0 {
		binary.LittleEndian.PutUint64(word[:], r.src.Uint64())
		copy(p, word[:len(p)])
	}
}

// IntN returns a uniform value in [0, n) drawn from r.
func IntN(r Rand, n int) int { return int(r.Int64N(int64(n))) }

// Float64 returns a uniform value in [0, 1) drawn from r using its top 53 bits.
func Float64(r Rand) float64 { return float64(r.Uint64()>>11) / (1 << 53) }

// Perm returns a uniformly shuffled permutation of [0, n) drawn from r.
func Perm(r Rand, n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := IntN(r, i+1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}
