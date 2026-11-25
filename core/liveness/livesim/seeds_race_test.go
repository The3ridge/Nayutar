//go:build race

package livesim_test

// Sim runs on one goroutine, so -race only needs a couple of parallel seeds.
const defaultSeedCount = 2
