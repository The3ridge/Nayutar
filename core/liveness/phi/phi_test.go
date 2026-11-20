package phi

import (
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	valid := DefaultConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v, want nil", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "threshold low", mutate: func(c *Config) { c.Threshold = 0 }, wantErr: true},
		{name: "threshold min", mutate: func(c *Config) { c.Threshold = 1 }, wantErr: false},
		{name: "threshold max", mutate: func(c *Config) { c.Threshold = 16 }, wantErr: false},
		{name: "threshold high", mutate: func(c *Config) { c.Threshold = 17 }, wantErr: true},
		{name: "max samples low", mutate: func(c *Config) { c.MaxSamples = 0 }, wantErr: true},
		{name: "max samples min", mutate: func(c *Config) { c.MaxSamples = 1 }, wantErr: false},
		{name: "max samples max", mutate: func(c *Config) { c.MaxSamples = 1000 }, wantErr: false},
		{name: "max samples high", mutate: func(c *Config) { c.MaxSamples = 1001 }, wantErr: true},
		{name: "min std dev neg", mutate: func(c *Config) { c.MinStdDev = -1 }, wantErr: true},
		{name: "min std dev zero", mutate: func(c *Config) { c.MinStdDev = 0 }, wantErr: false},
		{name: "min std dev max", mutate: func(c *Config) { c.MinStdDev = 600000 }, wantErr: false},
		{name: "min std dev high", mutate: func(c *Config) { c.MinStdDev = 600001 }, wantErr: true},
		{name: "pause neg", mutate: func(c *Config) { c.AcceptablePause = -1 }, wantErr: true},
		{name: "pause zero", mutate: func(c *Config) { c.AcceptablePause = 0 }, wantErr: false},
		{name: "pause max", mutate: func(c *Config) { c.AcceptablePause = 600000 }, wantErr: false},
		{name: "pause high", mutate: func(c *Config) { c.AcceptablePause = 600001 }, wantErr: true},
		{name: "first interval low", mutate: func(c *Config) { c.FirstInterval = 3 }, wantErr: true},
		{name: "first interval min", mutate: func(c *Config) { c.FirstInterval = 4; c.MaxInterval = 5 }, wantErr: false},
		{name: "first interval max allowed with max interval", mutate: func(c *Config) { c.FirstInterval = 480000; c.MaxInterval = 600000 }, wantErr: false},
		{name: "first interval over 480000 leaves empty max interval range", mutate: func(c *Config) { c.FirstInterval = 480001; c.MaxInterval = 600000 }, wantErr: true},
		{name: "first interval high", mutate: func(c *Config) { c.FirstInterval = 600001 }, wantErr: true},
		{name: "max interval below first+first/4", mutate: func(c *Config) { c.FirstInterval = 1000; c.MaxInterval = 1249 }, wantErr: true},
		{name: "max interval equal first+first/4", mutate: func(c *Config) { c.FirstInterval = 1000; c.MaxInterval = 1250 }, wantErr: false},
		{name: "max interval above 600000", mutate: func(c *Config) { c.MaxInterval = 600001 }, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestSanityValue(t *testing.T) {
	w := New(DefaultConfig())
	if w.Started() {
		t.Fatal("new window should not be started")
	}
	if w.Last() != 0 {
		t.Fatalf("new window Last() = %d, want 0", w.Last())
	}
	if got := w.Samples(); len(got) != 0 {
		t.Fatalf("new window Samples() = %v, want empty", got)
	}
	if w.Suspect(0) {
		t.Fatal("unstarted window must not suspect")
	}

	w.Heartbeat(0)
	if !w.Started() || w.Last() != 0 {
		t.Fatalf("after first heartbeat: Started=%v Last=%d, want true, 0", w.Started(), w.Last())
	}
	if got, want := w.Samples(), []int64{750, 1250}; !slices.Equal(got, want) {
		t.Fatalf("initial samples = %v, want %v", got, want)
	}
	if w.Suspect(5403) {
		t.Fatal("Suspect(5403) = true, want false")
	}
	if !w.Suspect(5404) {
		t.Fatal("Suspect(5404) = false, want true")
	}
}

func TestHeartbeatRecordingAndEviction(t *testing.T) {
	t.Run("eviction at MaxSamples", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxSamples = 4
		w := New(cfg)
		w.Heartbeat(100)  // [750, 1250]
		w.Heartbeat(900)  // d=800 -> [750, 1250, 800]
		w.Heartbeat(1800) // d=900 -> [750, 1250, 800, 900]
		w.Heartbeat(2900) // d=1100 -> [1250, 800, 900, 1100]
		w.Heartbeat(4100) // d=1200 -> [800, 900, 1100, 1200]
		if got, want := w.Samples(), []int64{800, 900, 1100, 1200}; !slices.Equal(got, want) {
			t.Fatalf("Samples() = %v, want %v", got, want)
		}
		if w.Last() != 4100 {
			t.Fatalf("Last() = %d, want 4100", w.Last())
		}
		// Ensure Samples returns a copy.
		s := w.Samples()
		s[0] = 999999
		if w.Samples()[0] != 800 {
			t.Fatal("Samples() did not return an independent copy")
		}
	})

	t.Run("MaxSamples=1 on first heartbeat", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxSamples = 1
		w := New(cfg)
		w.Heartbeat(10)
		if got, want := w.Samples(), []int64{1250}; !slices.Equal(got, want) {
			t.Fatalf("Samples() = %v, want %v", got, want)
		}
	})

	t.Run("intervals skipped when suspect or over MaxInterval but last updates", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxInterval = 4500
		cfg.AcceptablePause = 5000
		w := New(cfg)
		w.Heartbeat(0) // [750, 1250]
		// d=4600 > MaxInterval(4500) even though not suspect (t <= AcceptablePause).
		w.Heartbeat(4600)
		if got, want := w.Samples(), []int64{750, 1250}; !slices.Equal(got, want) {
			t.Fatalf("after d>MaxInterval Samples() = %v, want %v", got, want)
		}
		if w.Last() != 4600 {
			t.Fatalf("Last() = %d, want 4600", w.Last())
		}

		// Now allow large MaxInterval, trigger Suspect=true before Heartbeat.
		cfg2 := DefaultConfig()
		cfg2.MaxInterval = 20000
		w2 := New(cfg2)
		w2.Heartbeat(0)
		if !w2.Suspect(6000) {
			t.Fatal("expected Suspect(6000) to be true")
		}
		w2.Heartbeat(6000)
		if got, want := w2.Samples(), []int64{750, 1250}; !slices.Equal(got, want) {
			t.Fatalf("after suspect heartbeat Samples() = %v, want %v", got, want)
		}
		if w2.Last() != 6000 {
			t.Fatalf("Last() = %d, want 6000", w2.Last())
		}
		// Immediately after, not suspect and interval 1000 is recorded.
		w2.Heartbeat(7000)
		if got, want := w2.Samples(), []int64{750, 1250, 1000}; !slices.Equal(got, want) {
			t.Fatalf("after recovery heartbeat Samples() = %v, want %v", got, want)
		}
	})

	t.Run("now < last ignored", func(t *testing.T) {
		w := New(DefaultConfig())
		w.Heartbeat(1000)
		if w.Suspect(999) {
			t.Fatal("Suspect(999) when last=1000 must be false")
		}
		w.Heartbeat(999)
		if w.Last() != 1000 {
			t.Fatalf("Last() changed to %d after earlier heartbeat", w.Last())
		}
		if got, want := w.Samples(), []int64{750, 1250}; !slices.Equal(got, want) {
			t.Fatalf("Samples() = %v, want %v", got, want)
		}
	})
}

func bigSuspectRef(cfg Config, samples []int64, last, now int64, started bool) bool {
	if !started || now < last {
		return false
	}
	t := new(big.Int).Sub(big.NewInt(now), big.NewInt(last))
	n := big.NewInt(int64(len(samples)))

	s := big.NewInt(0)
	q := big.NewInt(0)
	for _, x := range samples {
		bx := big.NewInt(x)
		s.Add(s, bx)
		q.Add(q, new(big.Int).Mul(bx, bx))
	}

	// A = n*(t - AcceptablePause) - S
	a := new(big.Int).Sub(t, big.NewInt(cfg.AcceptablePause))
	a.Mul(n, a)
	a.Sub(a, s)
	if a.Sign() <= 0 {
		return false
	}

	// V = max(n*Q - S*S, n*n*MinStdDev*MinStdDev)
	v1 := new(big.Int).Mul(n, q)
	v1.Sub(v1, new(big.Int).Mul(s, s))

	minTerm := new(big.Int).Mul(n, big.NewInt(cfg.MinStdDev))
	v2 := new(big.Int).Mul(minTerm, minTerm)

	v := v1
	if v2.Cmp(v1) > 0 {
		v = v2
	}

	lhs := new(big.Int).Mul(a, a)
	lhs.Mul(lhs, big.NewInt(1000))

	rhs := new(big.Int).Mul(big.NewInt(int64(zTable[cfg.Threshold])), v)
	return lhs.Cmp(rhs) > 0
}

func TestBigIntDifferentialRandomized(t *testing.T) {
	const numCases = 25_000
	rng := rand.New(rand.NewPCG(0x51a7c0de, 0x9e3779b97f4a7c15))

	for i := range numCases {
		first := int64(4 + rng.IntN(5000))
		if rng.IntN(10) == 0 {
			first = 480000
		}
		minMax := first + first/4
		maxInt := minMax + rng.Int64N(maxTimeMS-minMax+1)
		cfg := Config{
			Threshold:       1 + rng.IntN(16),
			MaxSamples:      1 + rng.IntN(1000),
			MinStdDev:       rng.Int64N(maxTimeMS + 1),
			AcceptablePause: rng.Int64N(maxTimeMS + 1),
			FirstInterval:   first,
			MaxInterval:     maxInt,
		}
		if rng.IntN(8) == 0 {
			cfg.MinStdDev = 0
		}
		if rng.IntN(8) == 0 {
			cfg.MaxSamples = 1
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("case %d generated invalid config %+v: %v", i, cfg, err)
		}

		w := New(cfg)
		start := int64(0)
		if rng.IntN(4) == 0 {
			start = rng.Int64N(1 << 40)
		}
		w.Heartbeat(start)
		last := start
		steps := 1 + rng.IntN(25)
		for range steps {
			var dt int64
			switch rng.IntN(4) {
			case 0:
				dt = 0
			case 1:
				dt = rng.Int64N(cfg.FirstInterval + 500)
			case 2:
				dt = rng.Int64N(cfg.MaxInterval + 1)
			default:
				dt = rng.Int64N(cfg.MaxInterval*2 + 1)
			}
			now := last + dt
			if now > 1<<62 {
				break
			}
			w.Heartbeat(now)
			last = w.Last()
		}

		samples := w.Samples()
		var testNow int64
		switch rng.IntN(6) {
		case 0:
			testNow = max(0, last-1)
		case 1:
			testNow = last + cfg.AcceptablePause
		case 2:
			testNow = last + cfg.AcceptablePause + rng.Int64N(50000)
		case 3:
			// Near the exact boundary for the current samples.
			var sum int64
			for _, x := range samples {
				sum += x
			}
			mean := sum / int64(len(samples))
			testNow = last + cfg.AcceptablePause + mean + rng.Int64N(20000)
		case 4:
			testNow = 1 << 62
		default:
			testNow = min(1<<62, last+rng.Int64N(1<<42))
		}

		got := w.Suspect(testNow)
		want := bigSuspectRef(cfg, samples, w.Last(), testNow, w.Started())
		if got != want {
			t.Fatalf("case %d mismatch: cfg=%+v samples=%v last=%d testNow=%d got=%v want=%v",
				i, cfg, samples, w.Last(), testNow, got, want)
		}
	}

	// Explicit 2^62 and exact-equality edge cases.
	cfg := DefaultConfig()
	w := New(cfg)
	w.Heartbeat(0)
	if !w.Suspect(1 << 62) {
		t.Fatal("Suspect(2^62) = false, want true")
	}
	if !bigSuspectRef(cfg, w.Samples(), 0, 1<<62, true) {
		t.Fatal("big reference Suspect(2^62) = false")
	}
	// Exact zero variance (MinStdDev = 0, MaxSamples = 1).
	cfgZero := Config{
		Threshold:       8,
		MaxSamples:      1,
		MinStdDev:       0,
		AcceptablePause: 100,
		FirstInterval:   400,
		MaxInterval:     1000,
	}
	wz := New(cfgZero)
	wz.Heartbeat(0) // samples = [500]
	// At now = 600: t = 600, A = 1*(600-100)-500 = 0 -> false.
	if wz.Suspect(600) {
		t.Fatal("zero variance at exact mean+pause must be false")
	}
	// At now = 601: t = 601, A = 1 -> 1000*1 > 0 -> true.
	if !wz.Suspect(601) {
		t.Fatal("zero variance 1ms past mean+pause must be true")
	}
}

func TestFloat64CrossCheck(t *testing.T) {
	const z8 = 5.612001

	cases := []struct {
		name      string
		cfg       Config
		intervals []int64
	}{
		{
			name:      "default initial",
			cfg:       DefaultConfig(),
			intervals: nil,
		},
		{
			name:      "steady 1000ms clamped by MinStdDev",
			cfg:       DefaultConfig(),
			intervals: []int64{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000},
		},
		{
			name:      "variable intervals",
			cfg:       Config{Threshold: 8, MaxSamples: 50, MinStdDev: 50, AcceptablePause: 1500, FirstInterval: 1000, MaxInterval: 10000},
			intervals: []int64{900, 1100, 1400, 800, 1200, 1500, 1000, 950},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := New(tc.cfg)
			now := int64(0)
			w.Heartbeat(now)
			for _, d := range tc.intervals {
				now += d
				w.Heartbeat(now)
			}

			samples := w.Samples()
			n := float64(len(samples))
			var sum, sumSq float64
			for _, x := range samples {
				fx := float64(x)
				sum += fx
				sumSq += fx * fx
			}
			mean := sum / n
			variance := sumSq/n - mean*mean
			sigma := math.Sqrt(variance)
			if minSD := float64(tc.cfg.MinStdDev); minSD > sigma {
				sigma = minSD
			}
			floatBoundary := float64(now) + mean + float64(tc.cfg.AcceptablePause) + z8*sigma

			// Binary search for the exact integer transition point.
			lo := now
			hi := now + int64(math.Ceil(floatBoundary-float64(now))) + 1000
			for lo < hi {
				mid := lo + (hi-lo)/2
				if w.Suspect(mid) {
					hi = mid
				} else {
					lo = mid + 1
				}
			}
			if w.Suspect(lo-1) || !w.Suspect(lo) {
				t.Fatalf("binary search did not find transition: Suspect(%d)=%v Suspect(%d)=%v",
					lo-1, w.Suspect(lo-1), lo, w.Suspect(lo))
			}

			diff := math.Abs(float64(lo) - floatBoundary)
			if diff > 1.0 {
				t.Fatalf("integer boundary %d differs from float boundary %.4f by %.4f ms (> 1 ms)",
					lo, floatBoundary, diff)
			}
		})
	}
}
