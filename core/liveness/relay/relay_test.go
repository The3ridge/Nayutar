package relay

import (
	"slices"
	"testing"
)

func TestTokenSequenceAndPending(t *testing.T) {
	r := New(400, 100)
	if got := r.Pending(); got != 0 {
		t.Fatalf("initial Pending() = %d, want 0", got)
	}

	t1 := r.Request(100, "agg-a", false, "node-1", 1)
	t2 := r.Request(105, "node-2", true, "node-3", 7)
	t3 := r.Request(110, "agg-b", false, "node-4", 2)

	if t1 != 1 || t2 != 2 || t3 != 3 {
		t.Fatalf("tokens = (%d, %d, %d), want (1, 2, 3)", t1, t2, t3)
	}
	if got := r.Pending(); got != 3 {
		t.Fatalf("Pending() = %d, want 3", got)
	}

	if _, ok := r.Ack(120, "node-1", t1); !ok {
		t.Fatal("Ack(t1) failed")
	}
	if got := r.Pending(); got != 2 {
		t.Fatalf("Pending() after Ack = %d, want 2", got)
	}

	t4 := r.Request(125, "agg-c", false, "node-5", 3)
	if t4 != 4 {
		t.Fatalf("t4 = %d, want 4", t4)
	}
	if got := r.Pending(); got != 3 {
		t.Fatalf("Pending() = %d, want 3", got)
	}
}

func TestAckSuccessAndFailures(t *testing.T) {
	r := New(400, 100)
	tok := r.Request(1000, "agg-a", false, "node-9", 42)
	tokNode := r.Request(1000, "node-7", true, "node-8", 9)

	// Unknown token.
	if res, ok := r.Ack(1100, "node-9", 999); ok || res != (Result{}) {
		t.Fatalf("Ack(unknown) = (%+v, %v), want (zero, false)", res, ok)
	}

	// Wrong target does not consume the probe.
	if res, ok := r.Ack(1100, "node-wrong", tok); ok || res != (Result{}) {
		t.Fatalf("Ack(wrong target) = (%+v, %v), want (zero, false)", res, ok)
	}
	if got := r.Pending(); got != 2 {
		t.Fatalf("Pending() after wrong target = %d, want 2", got)
	}

	// Successful Ack just before deadline (1399 < 1400).
	res, ok := r.Ack(1399, "node-9", tok)
	want := Result{
		Requester:     "agg-a",
		RequesterNode: false,
		Target:        "node-9",
		Round:         42,
		OK:            true,
	}
	if !ok || res != want {
		t.Fatalf("Ack(success) = (%+v, %v), want (%+v, true)", res, ok, want)
	}
	if got := r.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}

	// Second Ack for same token fails.
	if res2, ok2 := r.Ack(1399, "node-9", tok); ok2 || res2 != (Result{}) {
		t.Fatalf("second Ack = (%+v, %v), want (zero, false)", res2, ok2)
	}

	// Ack preserving RequesterNode == true.
	resNode, okNode := r.Ack(1200, "node-8", tokNode)
	wantNode := Result{
		Requester:     "node-7",
		RequesterNode: true,
		Target:        "node-8",
		Round:         9,
		OK:            true,
	}
	if !okNode || resNode != wantNode {
		t.Fatalf("Ack(node requester) = (%+v, %v), want (%+v, true)", resNode, okNode, wantNode)
	}
}

func TestAckAtOrAfterExpiry(t *testing.T) {
	r := New(400, 100)
	tok1 := r.Request(100, "agg-a", false, "node-1", 1) // deadline 500
	tok2 := r.Request(100, "agg-b", false, "node-2", 2) // deadline 500

	// Ack exactly at deadline (before Expire is called) must fail and keep the probe for Expire.
	if res, ok := r.Ack(500, "node-1", tok1); ok || res != (Result{}) {
		t.Fatalf("Ack at deadline = (%+v, %v), want (zero, false)", res, ok)
	}
	if got := r.Pending(); got != 2 {
		t.Fatalf("Pending() = %d, want 2", got)
	}

	// Expire removes both and reports OK: false.
	expired := r.Expire(500)
	want := []Result{
		{Requester: "agg-a", RequesterNode: false, Target: "node-1", Round: 1, OK: false},
		{Requester: "agg-b", RequesterNode: false, Target: "node-2", Round: 2, OK: false},
	}
	if !slices.Equal(expired, want) {
		t.Fatalf("Expire(500) = %+v, want %+v", expired, want)
	}
	if got := r.Pending(); got != 0 {
		t.Fatalf("Pending() after Expire = %d, want 0", got)
	}

	// Ack after Expire also fails.
	if res, ok := r.Ack(501, "node-2", tok2); ok || res != (Result{}) {
		t.Fatalf("Ack after Expire = (%+v, %v), want (zero, false)", res, ok)
	}
}

func TestExpireBoundaryAndAscendingTokenOrder(t *testing.T) {
	r := New(100, 30)
	_ = r.Request(50, "agg-a", false, "node-1", 10) // tok 1, deadline 150
	_ = r.Request(40, "node-x", true, "node-2", 20) // tok 2, deadline 140
	_ = r.Request(50, "agg-b", false, "node-3", 30) // tok 3, deadline 150
	_ = r.Request(60, "agg-c", false, "node-4", 40) // tok 4, deadline 160

	// Just before earliest deadline (139 < 140): nothing expires.
	if got := r.Expire(139); len(got) != 0 {
		t.Fatalf("Expire(139) = %+v, want empty", got)
	}
	if got := r.Pending(); got != 4 {
		t.Fatalf("Pending() = %d, want 4", got)
	}

	// Exactly at 150: tokens 1, 2, 3 expire in ascending token order (1, 2, 3), even though 2 had an earlier deadline.
	got := r.Expire(150)
	want := []Result{
		{Requester: "agg-a", RequesterNode: false, Target: "node-1", Round: 10, OK: false},
		{Requester: "node-x", RequesterNode: true, Target: "node-2", Round: 20, OK: false},
		{Requester: "agg-b", RequesterNode: false, Target: "node-3", Round: 30, OK: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Expire(150) = %+v, want %+v", got, want)
	}
	if got := r.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}

	// Calling Expire(150) again yields nothing.
	if again := r.Expire(150); len(again) != 0 {
		t.Fatalf("second Expire(150) = %+v, want empty", again)
	}

	// Token 4 expires at 160.
	got4 := r.Expire(160)
	want4 := []Result{
		{Requester: "agg-c", RequesterNode: false, Target: "node-4", Round: 40, OK: false},
	}
	if !slices.Equal(got4, want4) {
		t.Fatalf("Expire(160) = %+v, want %+v", got4, want4)
	}
	if got := r.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0", got)
	}
}

func TestResendTimingOrderAndTokenReuse(t *testing.T) {
	r := New(300, 100)
	tok1 := r.Request(0, "agg-a", false, "node-1", 1) // nextPing 100, deadline 300
	tok2 := r.Request(0, "agg-b", false, "node-2", 2) // nextPing 100, deadline 300

	// Nothing before interval.
	if got := r.Resend(99); len(got) != 0 {
		t.Fatalf("Resend(99) = %+v, want empty", got)
	}

	// First resend at 100: ascending by token, reusing the original tokens.
	got100 := r.Resend(100)
	want100 := []Ping{
		{Target: "node-1", Token: tok1},
		{Target: "node-2", Token: tok2},
	}
	if !slices.Equal(got100, want100) {
		t.Fatalf("Resend(100) = %+v, want %+v", got100, want100)
	}

	// Calling again at same timestamp or before next interval produces nothing.
	if got := r.Resend(100); len(got) != 0 {
		t.Fatalf("second Resend(100) = %+v, want empty", got)
	}
	if got := r.Resend(199); len(got) != 0 {
		t.Fatalf("Resend(199) = %+v, want empty", got)
	}

	// Late resend at 220 sets nextPing = 220 + 100 = 320.
	got220 := r.Resend(220)
	if !slices.Equal(got220, want100) {
		t.Fatalf("Resend(220) = %+v, want %+v", got220, want100)
	}

	// Even if a probe is due at or after deadline (e.g. tok3 requested at 150 with nextPing 250, deadline 450,
	// or a probe whose nextPing == deadline), Resend must not emit at now >= deadline.
	tok3 := r.Request(150, "agg-c", false, "node-3", 3) // nextPing 250, deadline 450
	if got := r.Resend(300); !slices.Equal(got, []Ping{{Target: "node-3", Token: tok3}}) {
		t.Fatalf("Resend(300) = %+v, want only tok3", got)
	}
	// tok1 and tok2 are at deadline 300 even before Expire is called; Resend(300) did not include them.
	if got := r.Resend(450); len(got) != 0 {
		t.Fatalf("Resend(450) at deadline = %+v, want empty", got)
	}
}

func TestAckAfterResendCompletesOnce(t *testing.T) {
	r := New(400, 100)
	tok := r.Request(100, "agg-a", false, "node-5", 11)

	pings := r.Resend(200)
	if len(pings) != 1 || pings[0] != (Ping{Target: "node-5", Token: tok}) {
		t.Fatalf("Resend(200) = %+v, want tok %d", pings, tok)
	}

	res, ok := r.Ack(230, "node-5", pings[0].Token)
	want := Result{
		Requester:     "agg-a",
		RequesterNode: false,
		Target:        "node-5",
		Round:         11,
		OK:            true,
	}
	if !ok || res != want {
		t.Fatalf("Ack after resend = (%+v, %v), want (%+v, true)", res, ok, want)
	}
	if got := r.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0", got)
	}

	// Second Ack, subsequent Resend, and subsequent Expire all emit nothing.
	if res2, ok2 := r.Ack(240, "node-5", tok); ok2 || res2 != (Result{}) {
		t.Fatalf("second Ack = (%+v, %v), want (zero, false)", res2, ok2)
	}
	if got := r.Resend(300); len(got) != 0 {
		t.Fatalf("Resend(300) after Ack = %+v, want empty", got)
	}
	if got := r.Expire(500); len(got) != 0 {
		t.Fatalf("Expire(500) after Ack = %+v, want empty", got)
	}
}
