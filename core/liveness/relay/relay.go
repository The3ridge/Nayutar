// Package relay tracks pending indirect liveness probes by token, re-send schedule, and expiration deadline.
package relay

import "slices"

// Result describes the outcome of a relayed probe once acknowledged or expired.
type Result struct {
	Requester     string // who asked
	RequesterNode bool
	Target        string // node that was pinged
	Round         uint64
	OK            bool
}

// Ping is a ping the caller must send to Target.
type Ping struct {
	Target string
	Token  uint64
}

type pendingProbe struct {
	requester     string
	requesterNode bool
	target        string
	round         uint64
	deadline      int64
	nextPing      int64
}

// Relay tracks pending indirect probes keyed by monotonic token.
type Relay struct {
	timeout  int64
	interval int64
	seq      uint64
	pending  map[uint64]pendingProbe
}

// New returns a relay whose probes time out after timeout ms and are re-pinged every interval ms.
func New(timeout, interval int64) *Relay {
	return &Relay{
		timeout:  timeout,
		interval: interval,
		pending:  make(map[uint64]pendingProbe),
	}
}

// Request registers a probe and returns the token to put in the first Ping sent to target.
func (r *Relay) Request(now int64, requester string, requesterNode bool, target string, round uint64) (token uint64) {
	r.seq++
	r.pending[r.seq] = pendingProbe{
		requester:     requester,
		requesterNode: requesterNode,
		target:        target,
		round:         round,
		deadline:      now + r.timeout,
		nextPing:      now + r.interval,
	}
	return r.seq
}

// Ack consumes the pending probe with this token if its target matches and it has not expired.
func (r *Relay) Ack(now int64, target string, token uint64) (Result, bool) {
	p, ok := r.pending[token]
	if !ok || p.target != target || now >= p.deadline {
		return Result{}, false
	}
	delete(r.pending, token)
	return Result{
		Requester:     p.requester,
		RequesterNode: p.requesterNode,
		Target:        p.target,
		Round:         p.round,
		OK:            true,
	}, true
}

// Expire removes every pending probe with now >= deadline and returns them with OK == false, ascending by token.
func (r *Relay) Expire(now int64) []Result {
	var tokens []uint64
	for tok, p := range r.pending {
		if now >= p.deadline {
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	slices.Sort(tokens)
	out := make([]Result, len(tokens))
	for i, tok := range tokens {
		p := r.pending[tok]
		delete(r.pending, tok)
		out[i] = Result{
			Requester:     p.requester,
			RequesterNode: p.requesterNode,
			Target:        p.target,
			Round:         p.round,
			OK:            false,
		}
	}
	return out
}

// Resend returns a Ping for every pending probe with nextPing <= now < deadline, ascending by token,
// and advances its nextPing to now + interval.
func (r *Relay) Resend(now int64) []Ping {
	var tokens []uint64
	for tok, p := range r.pending {
		if p.nextPing <= now && now < p.deadline {
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	slices.Sort(tokens)
	out := make([]Ping, len(tokens))
	for i, tok := range tokens {
		p := r.pending[tok]
		p.nextPing = now + r.interval
		r.pending[tok] = p
		out[i] = Ping{
			Target: p.target,
			Token:  tok,
		}
	}
	return out
}

// Pending returns the number of currently pending probes.
func (r *Relay) Pending() int {
	return len(r.pending)
}
