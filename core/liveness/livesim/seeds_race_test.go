//go:build race

package livesim_test

// The simulation runs on one goroutine, so the race detector only needs a few seeds.
const defaultSeedCount = 5
