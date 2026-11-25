package storetest

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestPartyBarrierMultiWave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			parties = 4
			waves   = 3
		)
		b := newPartyBarrier(parties)
		var passed [waves]atomic.Int32
		var gateLast [waves]chan struct{}
		var gateNext [waves]chan struct{}
		for w := range waves {
			gateLast[w] = make(chan struct{})
			gateNext[w] = make(chan struct{})
		}

		var wg sync.WaitGroup
		for id := range parties {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for w := range waves {
					if id == parties-1 {
						<-gateLast[w]
					}
					b.arrive()
					passed[w].Add(1)
					<-gateNext[w]
				}
			}()
		}

		for w := range waves {
			synctest.Wait()
			if got := passed[w].Load(); got != 0 {
				t.Fatalf("wave %d: %d parties passed arrive before last party arrived, want 0", w, got)
			}
			close(gateLast[w])
			synctest.Wait()
			if got := passed[w].Load(); got != parties {
				t.Fatalf("wave %d: %d parties unblocked after last party arrived, want %d", w, got, parties)
			}
			close(gateNext[w])
		}
		wg.Wait()
	})
}

func TestPartyBarrierDepartUnblocksAndDegrades(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newPartyBarrier(3)
		var arrived atomic.Int32
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b.arrive()
				arrived.Add(1)
			}()
		}

		synctest.Wait()
		if got := arrived.Load(); got != 0 {
			t.Fatalf("got %d parties unblocked before 3rd departed, want 0", got)
		}

		b.depart()
		synctest.Wait()
		if got := arrived.Load(); got != 2 {
			t.Fatalf("got %d parties unblocked after 3rd departed, want 2", got)
		}
		wg.Wait()

		b.depart()
		for range 3 {
			b.arrive()
		}
	})
}

func TestPartyBarrierDegenerateCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, n := range []int{0, 1} {
			b := newPartyBarrier(n)
			b.arrive()
			b.arrive()
			b.depart()
			b.arrive()
			b.depart()
		}
	})
}

func TestPartyBarrierUngatedRapidWaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			parties = 4
			waves   = 50
		)
		b := newPartyBarrier(parties)
		var completed [parties]atomic.Int32
		var waveEntries [waves]atomic.Int32

		var wg sync.WaitGroup
		for id := range parties {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for w := range waves {
					waveEntries[w].Add(1)
					b.arrive()
					if id == 0 {
						runtime.Gosched()
					}
					completed[id].Add(1)
				}
			}()
		}
		wg.Wait()

		for id := range parties {
			if got := completed[id].Load(); got != waves {
				t.Fatalf("party %d completed %d waves, want %d", id, got, waves)
			}
		}
		for w := range waves {
			if got := waveEntries[w].Load(); got != parties {
				t.Fatalf("wave %d had %d entries, want %d", w, got, parties)
			}
		}
	})
}
