package detector

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/liveness/phi"
)

// refCore is a reference implementation of the detector state machine used as a
// differential test oracle.
type refCore struct {
	cfg       Config
	now       int64
	known     map[string]Record
	tracks    map[string]*refTrack
	seq       uint64
	suspended bool
}

type refTrack struct {
	window        *phi.Window
	streak        int
	heard         bool
	backoff       int
	notReadySince int64
	round         *refRound
	nextRoundAt   int64
	nextReadyAt   int64
	nextEvictAt   int64
}

type refRound struct {
	id           uint64
	deadline     int64
	modRev       int64
	voters       []string
	voted        []string
	suspectVotes int
	probers      []prober
	failed       []prober
	doubted      bool
}

func newRefCore(cfg Config) *refCore {
	return &refCore{
		cfg:    cfg,
		known:  make(map[string]Record),
		tracks: make(map[string]*refTrack),
	}
}

func (r *refCore) clamp(now int64) int64 {
	if now > r.now {
		r.now = now
	}
	return r.now
}

func (r *refCore) ranked(node string) []string {
	var out []string
	for _, x := range r.cfg.Ranked(node) {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (r *refCore) owners(node string) []string {
	rk := r.ranked(node)
	if len(rk) > r.cfg.Owners {
		return rk[:r.cfg.Owners]
	}
	return rk
}

func (r *refCore) owns(node string) bool {
	return slices.Contains(r.owners(node), r.cfg.Self)
}

func (r *refCore) quorum(node string) int {
	return len(r.owners(node))/2 + 1
}

func (r *refCore) voters(node string) []string {
	var out []string
	for _, o := range r.owners(node) {
		if o != r.cfg.Self {
			out = append(out, o)
		}
	}
	return out
}

func (r *refCore) peerProbers(node string) []string {
	rk := r.ranked(node)
	ow := r.owners(node)
	var out []string
	for _, x := range rk[len(ow):] {
		if x != r.cfg.Self && len(out) < r.cfg.PeerProbers {
			out = append(out, x)
		}
	}
	for _, v := range r.voters(node) {
		if len(out) < r.cfg.PeerProbers {
			out = append(out, v)
		}
	}
	return out
}

func refRotate(cands []string, node string) []string {
	slices.Sort(cands)
	idx := -1
	for i, c := range cands {
		if c > node {
			idx = i
			break
		}
	}
	if idx > 0 {
		return append(cands[idx:], cands[:idx]...)
	}
	return cands
}

func (r *refCore) rackProbers(node string, now int64) []string {
	target := r.known[node]
	var cands []string
	for m, rec := range r.known {
		if m != node && rec.Rack == target.Rack && rec.State == StateReady {
			if tr := r.tracks[m]; tr != nil && tr.window.Suspect(now) {
				continue
			}
			cands = append(cands, m)
		}
	}
	cands = refRotate(cands, node)
	if len(cands) > r.cfg.RackProbers {
		cands = cands[:r.cfg.RackProbers]
	}
	return cands
}

func (r *refCore) witnessProbers(node string, now int64) []string {
	target := r.known[node]
	var cands []string
	for m, rec := range r.known {
		if m != node && rec.State == StateReady && rec.Rack != target.Rack {
			if tr := r.tracks[m]; tr != nil && (!tr.heard || tr.window.Suspect(now)) {
				continue
			}
			cands = append(cands, m)
		}
	}
	cands = refRotate(cands, node)
	if len(cands) > r.cfg.WitnessProbers {
		cands = cands[:r.cfg.WitnessProbers]
	}
	return cands
}

func (r *refCore) probers(node string, now int64) []prober {
	var out []prober
	for _, p := range r.peerProbers(node) {
		out = append(out, prober{id: p, isNode: false})
	}
	for _, p := range r.rackProbers(node, now) {
		out = append(out, prober{id: p, isNode: true})
	}
	for _, p := range r.witnessProbers(node, now) {
		out = append(out, prober{id: p, isNode: true})
	}
	return out
}

func (r *refCore) delay(k int) int64 {
	if k > 16 {
		k = 16
	}
	d := r.cfg.RetryInterval << k
	if d > r.cfg.MaxRoundBackoff {
		return r.cfg.MaxRoundBackoff
	}
	return d
}

func (r *refCore) bump(tr *refTrack, now int64) {
	tr.backoff = min(tr.backoff+1, 16)
	tr.nextRoundAt = max(tr.nextRoundAt, now+r.delay(tr.backoff))
}

func (r *refCore) Observed(now int64, node string, rec Record) {
	now = r.clamp(now)
	prev, had := r.known[node]
	r.known[node] = rec
	tr := r.tracks[node]
	if tr == nil {
		if r.owns(node) && (rec.State == StateReady || rec.State == StateNotReady) {
			w := phi.New(r.cfg.Phi)
			w.Heartbeat(now)
			r.tracks[node] = &refTrack{window: w, notReadySince: now}
		}
		return
	}
	if tr.round != nil && (rec.State != StateReady || rec.ModRev != tr.round.modRev) {
		tr.round = nil
	}
	if rec.State == StateNotReady && (!had || prev.State != StateNotReady || prev.ModRev != rec.ModRev) {
		tr.streak = 0
		tr.notReadySince = now
	}
	if rec.State == StateReady && had && prev.State == StateNotReady && tr.window.Suspect(now) {
		r.bump(tr, now)
	}
}

func (r *refCore) Forgotten(now int64, node string) {
	_ = r.clamp(now)
	delete(r.known, node)
	delete(r.tracks, node)
}

func (r *refCore) Heartbeat(now int64, node string) (HeartbeatStatus, []Action) {
	now = r.clamp(now)
	rec, ok := r.known[node]
	if !ok {
		return HeartbeatUnknownNode, nil
	}
	if !r.owns(node) {
		return HeartbeatNotOwner, nil
	}
	tr := r.tracks[node]
	if tr == nil {
		tr = &refTrack{window: phi.New(r.cfg.Phi)}
		r.tracks[node] = tr
	}
	was := tr.window.Suspect(now)
	tr.window.Heartbeat(now)
	if was {
		tr.streak = 1
	} else {
		tr.streak = min(tr.streak+1, r.cfg.ReadyHeartbeats)
	}
	tr.round = nil
	tr.heard = true
	tr.backoff = 0
	tr.nextRoundAt = 0
	var acts []Action
	if rec.State != StateReady && tr.streak >= r.cfg.ReadyHeartbeats && now >= tr.nextReadyAt {
		acts = append(acts, Action{Kind: ActProposeReady, Node: node, ModRev: rec.ModRev})
		tr.nextReadyAt = now + r.cfg.RetryInterval
	}
	return HeartbeatAccepted, acts
}

func (r *refCore) Tick(now int64) []Action {
	now = r.clamp(now)
	var tracked []string
	for n := range r.tracks {
		tracked = append(tracked, n)
	}
	slices.Sort(tracked)

	alive, suspected := 0, 0
	for _, n := range tracked {
		if r.known[n].State == StateReady {
			alive++
			if r.tracks[n].window.Suspect(now) {
				suspected++
			}
		}
	}
	r.suspended = (suspected >= r.cfg.SelfSuspectMinNodes && suspected*100 >= r.cfg.SelfSuspectPercent*alive) ||
		(alive >= 2 && suspected == alive)

	var acts []Action
	for _, n := range tracked {
		tr := r.tracks[n]
		rec := r.known[n]
		if tr.round != nil {
			tr.round.doubted = tr.round.doubted || r.suspended
		}
		if tr.round != nil && now >= tr.round.deadline {
			failures := len(tr.round.failed)
			nodeFailures := 0
			for _, f := range tr.round.failed {
				if f.isNode {
					nodeFailures++
				}
			}
			nodeAsked := 0
			for _, p := range tr.round.probers {
				if p.isNode {
					nodeAsked++
				}
			}
			base := rec.State == StateReady && rec.ModRev == tr.round.modRev && 1+tr.round.suspectVotes >= r.quorum(n)
			var confirm bool
			if nodeAsked >= 1 {
				confirm = base && nodeFailures >= min(r.cfg.MinFailedProbes, nodeAsked)
			} else if tr.round.doubted {
				confirm = false
			} else {
				confirm = base && len(tr.round.probers) >= 1 && failures >= min(r.cfg.MinFailedProbes, len(tr.round.probers))
			}
			if confirm {
				acts = append(acts, Action{Kind: ActProposeNotReady, Node: n, ModRev: tr.round.modRev})
			}
			tr.round = nil
			tr.nextRoundAt = now + r.cfg.RetryInterval
		} else if tr.round == nil && rec.State == StateReady && tr.window.Suspect(now) && now >= tr.nextRoundAt {
			r.seq++
			id := r.cfg.RoundBase + r.seq
			v := r.voters(n)
			p := r.probers(n, now)
			tr.round = &refRound{
				id:       id,
				deadline: now + r.cfg.RoundTimeout,
				modRev:   rec.ModRev,
				voters:   v,
				probers:  p,
				doubted:  r.suspended,
			}
			for _, to := range v {
				acts = append(acts, Action{Kind: ActVoteRequest, To: to, Node: n, Round: id})
			}
			for _, pr := range p {
				acts = append(acts, Action{Kind: ActProbeRequest, To: pr.id, ToNode: pr.isNode, Node: n, Round: id})
			}
		}
		if !r.suspended && rec.State == StateNotReady && !rec.Evictable &&
			now-tr.notReadySince >= r.cfg.EvictionGrace && tr.window.Suspect(now) && now >= tr.nextEvictAt {
			acts = append(acts, Action{Kind: ActProposeEvictable, Node: n, ModRev: rec.ModRev})
			tr.nextEvictAt = now + r.cfg.EvictionRetryInterval
		}
	}
	return acts
}

func (r *refCore) VoteRequested(now int64, from, node string, round uint64) []Action {
	now = r.clamp(now)
	tr := r.tracks[node]
	rec, ok := r.known[node]
	var s, l bool
	if tr != nil && ok && rec.State == StateReady {
		s = tr.window.Suspect(now)
		l = tr.heard && !s
	}
	return []Action{{Kind: ActVote, To: from, Node: node, Round: round, Suspect: s, Alive: l}}
}

func (r *refCore) VoteReceived(now int64, from, node string, round uint64, suspect, alive bool) {
	now = r.clamp(now)
	tr := r.tracks[node]
	if tr == nil || tr.round == nil || tr.round.id != round {
		return
	}
	if !slices.Contains(tr.round.voters, from) {
		return
	}
	if alive {
		tr.round = nil
		r.bump(tr, now)
		return
	}
	if slices.Contains(tr.round.voted, from) {
		return
	}
	tr.round.voted = append(tr.round.voted, from)
	if suspect {
		tr.round.suspectVotes++
	}
}

func (r *refCore) ProbeResult(now int64, from string, fromNode bool, node string, round uint64, ok bool) {
	now = r.clamp(now)
	tr := r.tracks[node]
	if tr == nil || tr.round == nil || tr.round.id != round {
		return
	}
	p := prober{id: from, isNode: fromNode}
	if !slices.Contains(tr.round.probers, p) {
		return
	}
	if ok {
		tr.round = nil
		r.bump(tr, now)
		return
	}
	if slices.Contains(tr.round.failed, p) {
		return
	}
	tr.round.failed = append(tr.round.failed, p)
}

func (r *refCore) Snapshot() Snapshot {
	var names []string
	for n := range r.tracks {
		names = append(names, n)
	}
	slices.Sort(names)
	nodes := make([]NodeSnapshot, len(names))
	for i, n := range names {
		tr := r.tracks[n]
		var rid uint64
		var doubted bool
		if tr.round != nil {
			rid = tr.round.id
			doubted = tr.round.doubted
		}
		nodes[i] = NodeSnapshot{
			Node:          n,
			Streak:        tr.streak,
			Round:         rid,
			Samples:       len(tr.window.Samples()),
			Last:          tr.window.Last(),
			Backoff:       tr.backoff,
			Heard:         tr.heard,
			NextRoundAt:   tr.nextRoundAt,
			NextReadyAt:   tr.nextReadyAt,
			NextEvictAt:   tr.nextEvictAt,
			NotReadySince: tr.notReadySince,
			Doubted:       doubted,
		}
	}
	return Snapshot{Suspended: r.suspended, Nodes: nodes}
}

func TestRandomizedDifferentialOracle(t *testing.T) {
	const numSeqs = 2500
	const opsPerSeq = 200

	aggs := []string{"a", "b", "c", "d", "e"}
	nodes := []string{"n1", "n2", "n3", "n4", "n5", "n6"}
	racks := []string{"r1", "r2", "r3"}
	states := []State{StatePending, StateReady, StateNotReady}

	for seed := uint64(1); seed <= numSeqs; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed^0x517cc1b727220a95))

		unownedWitnessBias := seed%4 == 0
		owners := 1 + rng.IntN(4)
		witnessProbers := rng.IntN(4)
		if unownedWitnessBias {
			owners = 1 + rng.IntN(3)
			witnessProbers = 1 + rng.IntN(3)
		}

		rankedTable := make(map[string][]string, len(nodes))
		for idx, n := range nodes {
			perm := slices.Clone(aggs)
			rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			k := rng.IntN(len(perm) + 1)
			var rk []string
			for _, id := range perm[:k] {
				if unownedWitnessBias && idx > 0 && id == "a" {
					continue
				}
				rk = append(rk, id)
				if rng.IntN(5) == 0 {
					rk = append(rk, id)
				}
			}
			if unownedWitnessBias && idx == 0 && !slices.Contains(rk[:min(owners, len(rk))], "a") {
				rk = append([]string{"a"}, rk...)
			}
			rankedTable[n] = rk
		}

		retryInt := int64(50 + rng.IntN(300))
		maxBackoff := retryInt + int64(rng.IntN(3000))
		if rng.IntN(5) == 0 {
			maxBackoff = min(int64(maxBackoffIntervalMS), retryInt<<17)
		}
		var roundBase uint64
		if rng.IntN(3) == 0 {
			roundBase = uint64(rng.Int64N(1 << 40))
		}
		cfg := Config{
			Self:                  "a",
			Owners:                owners,
			PeerProbers:           rng.IntN(4),
			RackProbers:           rng.IntN(3),
			WitnessProbers:        witnessProbers,
			MinFailedProbes:       1 + rng.IntN(3),
			RoundTimeout:          int64(50 + rng.IntN(300)),
			RetryInterval:         retryInt,
			MaxRoundBackoff:       maxBackoff,
			EvictionRetryInterval: int64(50 + rng.IntN(600)),
			SelfSuspectPercent:    1 + rng.IntN(100),
			SelfSuspectMinNodes:   1 + rng.IntN(3),
			ReadyHeartbeats:       1 + rng.IntN(4),
			EvictionGrace:         int64(rng.IntN(2000)),
			RoundBase:             roundBase,
			Phi: phi.Config{
				Threshold:       1 + rng.IntN(8),
				MaxSamples:      2 + rng.IntN(20),
				MinStdDev:       int64(rng.IntN(100)),
				AcceptablePause: int64(100 + rng.IntN(400)),
				FirstInterval:   int64(50 + rng.IntN(150)),
				MaxInterval:     int64(500 + rng.IntN(2000)),
			},
			Ranked: func(node string) []string {
				return rankedTable[node]
			},
		}

		got, err := New(cfg)
		if err != nil {
			t.Fatalf("seed=%d invalid generated config: %v", seed, err)
		}
		want := newRefCore(cfg)

		now := int64(0)
		modRevs := make(map[string]int64, len(nodes))

		for step := range opsPerSeq {
			switch rng.IntN(12) {
			case 0:
				now = max(0, now-int64(1+rng.IntN(100)))
			case 1, 2:
				// Larger jumps make multiple nodes suspect simultaneously, triggering suspended ticks.
				now += int64(400 + rng.IntN(1400))
			default:
				now += int64(rng.IntN(120))
			}

			var gotStatus, wantStatus HeartbeatStatus
			var gotActs, wantActs []Action

			op := rng.IntN(100)
			switch {
			case (unownedWitnessBias && step < len(nodes)) || op < 16: // Observed (often changes ModRev or flips states)
				n := nodes[rng.IntN(len(nodes))]
				state := states[rng.IntN(len(states))]
				rack := racks[rng.IntN(len(racks))]
				if unownedWitnessBias && step < len(nodes) {
					n = nodes[step]
					state = StateReady
					rack = racks[step%len(racks)]
				}
				if rng.IntN(2) == 0 || modRevs[n] == 0 {
					modRevs[n] = int64(1 + step)
				}
				rec := Record{
					State:     state,
					Since:     int64(rng.IntN(100000)),
					Rack:      rack,
					Evictable: rng.IntN(4) == 0,
					ModRev:    modRevs[n],
				}
				got.Observed(now, n, rec)
				want.Observed(now, n, rec)

			case op < 19: // Forgotten
				n := nodes[rng.IntN(len(nodes))]
				got.Forgotten(now, n)
				want.Forgotten(now, n)

			case op < 38: // Heartbeat
				n := nodes[rng.IntN(len(nodes))]
				gotStatus, gotActs = got.Heartbeat(now, n)
				wantStatus, wantActs = want.Heartbeat(now, n)

			case op < 60: // Tick
				gotActs = got.Tick(now)
				wantActs = want.Tick(now)

			case op < 68: // VoteRequested
				from := aggs[rng.IntN(len(aggs))]
				n := nodes[rng.IntN(len(nodes))]
				rid := uint64(1 + rng.IntN(5))
				gotActs = got.VoteRequested(now, from, n, rid)
				wantActs = want.VoteRequested(now, from, n, rid)

			case op < 84: // VoteReceived (bias toward live rounds, including alive votes)
				n, rid, from := pickLiveRoundVoter(rng, want, nodes, aggs)
				suspect := rng.IntN(3) != 0
				alive := rng.IntN(4) == 0
				got.VoteReceived(now, from, n, rid, suspect, alive)
				want.VoteReceived(now, from, n, rid, suspect, alive)

			default: // ProbeResult (bias toward live rounds, frequent explicit failures)
				n, rid, from, fromNode := pickLiveRoundProber(rng, want, nodes, aggs)
				ok := rng.IntN(5) == 0
				got.ProbeResult(now, from, fromNode, n, rid, ok)
				want.ProbeResult(now, from, fromNode, n, rid, ok)
			}

			if gotStatus != wantStatus {
				t.Fatalf("seed=%d step=%d status=%v want=%v", seed, step, gotStatus, wantStatus)
			}
			if !slices.Equal(gotActs, wantActs) {
				t.Fatalf("seed=%d step=%d actions mismatch:\ngot:  %+v\nwant: %+v", seed, step, gotActs, wantActs)
			}
			if got.Suspended() != want.suspended {
				t.Fatalf("seed=%d step=%d suspended=%v want=%v", seed, step, got.Suspended(), want.suspended)
			}
			gotSnap := got.Snapshot()
			wantSnap := want.Snapshot()
			if gotSnap.Suspended != wantSnap.Suspended || !slices.Equal(gotSnap.Nodes, wantSnap.Nodes) {
				t.Fatalf("seed=%d step=%d snapshot mismatch:\ngot:  %+v\nwant: %+v",
					seed, step, gotSnap, wantSnap)
			}
		}
	}
}

func pickLiveRoundVoter(rng *rand.Rand, ref *refCore, nodes, aggs []string) (node string, rid uint64, from string) {
	var live []string
	for n, tr := range ref.tracks {
		if tr.round != nil {
			live = append(live, n)
		}
	}
	slices.Sort(live)
	if len(live) > 0 && rng.IntN(5) != 0 {
		node = live[rng.IntN(len(live))]
		rd := ref.tracks[node].round
		rid = rd.id
		if len(rd.voters) > 0 && rng.IntN(5) != 0 {
			from = rd.voters[rng.IntN(len(rd.voters))]
		} else {
			from = aggs[rng.IntN(len(aggs))]
		}
		return node, rid, from
	}
	return nodes[rng.IntN(len(nodes))], uint64(rng.IntN(max(1, int(ref.seq)+2))), aggs[rng.IntN(len(aggs))]
}

func pickLiveRoundProber(rng *rand.Rand, ref *refCore, nodes, aggs []string) (node string, rid uint64, from string, fromNode bool) {
	var live []string
	for n, tr := range ref.tracks {
		if tr.round != nil {
			live = append(live, n)
		}
	}
	slices.Sort(live)
	if len(live) > 0 && rng.IntN(5) != 0 {
		node = live[rng.IntN(len(live))]
		rd := ref.tracks[node].round
		rid = rd.id
		if len(rd.probers) > 0 && rng.IntN(5) != 0 {
			p := rd.probers[rng.IntN(len(rd.probers))]
			return node, rid, p.id, p.isNode
		}
		return node, rid, fmt.Sprintf("x%d", rng.IntN(3)), rng.IntN(2) == 0
	}
	return nodes[rng.IntN(len(nodes))], uint64(rng.IntN(max(1, int(ref.seq)+2))), aggs[rng.IntN(len(aggs))], rng.IntN(2) == 0
}
