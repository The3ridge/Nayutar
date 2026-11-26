//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

type op struct {
	Op     string `json:"op"`
	Expect *int   `json:"expect,omitempty"`
	Value  *int   `json:"value,omitempty"`
}

type testCase struct {
	ID  int  `json:"id"`
	Ops []op `json:"ops"`
}

type step struct {
	OK       bool `json:"ok"`
	Value    *int `json:"value"`
	Revision int  `json:"revision"`
}

type caseResult struct {
	ID    int    `json:"id"`
	Steps []step `json:"steps"`
}

type register struct {
	hasValue bool
	value    int
	revision int
}

type stepFunc func(state register, o op) (register, bool)

func stepRegister(state register, o op) (register, bool) {
	switch o.Op {
	case "put":
		state.hasValue = true
		state.value = *o.Value
		state.revision++
		return state, true
	case "delete":
		if !state.hasValue {
			return state, false
		}
		state.hasValue = false
		state.value = 0
		state.revision++
		return state, true
	case "cas":
		if state.revision != *o.Expect {
			return state, false
		}
		state.hasValue = true
		state.value = *o.Value
		state.revision++
		return state, true
	default:
		panic("unknown op: " + o.Op)
	}
}

func runRegister(tc testCase, fn stepFunc) caseResult {
	state := register{}
	steps := make([]step, len(tc.Ops))
	for i, o := range tc.Ops {
		next, ok := fn(state, o)
		state = next
		var val *int
		if state.hasValue {
			v := state.value
			val = &v
		}
		steps[i] = step{
			OK:       ok,
			Value:    val,
			Revision: state.revision,
		}
	}
	return caseResult{
		ID:    tc.ID,
		Steps: steps,
	}
}

func generateCases(seed uint64, n int) []testCase {
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	cases := make([]testCase, n)
	for i := range n {
		numOps := rng.IntN(17)
		ops := make([]op, numOps)
		for j := range numOps {
			switch rng.IntN(3) {
			case 0:
				v := rng.IntN(4)
				ops[j] = op{Op: "put", Value: &v}
			case 1:
				ops[j] = op{Op: "delete"}
			case 2:
				expect := rng.IntN(j + 2)
				v := rng.IntN(4)
				ops[j] = op{Op: "cas", Expect: &expect, Value: &v}
			}
		}
		cases[i] = testCase{
			ID:  i,
			Ops: ops,
		}
	}
	return cases
}

func encodeCases(t *testing.T, cases []testCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeStrictJSON(raw json.RawMessage, dst any) error {
	if isJSONNull(raw) {
		return fmt.Errorf("unexpected null value")
	}
	return json.Unmarshal(raw, dst)
}

func decodeNonNegInt(raw json.RawMessage) (int, error) {
	var n int
	if err := decodeStrictJSON(raw, &n); err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative integer %d", n)
	}
	return n, nil
}

func decodeResult(line []byte) (caseResult, error) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return caseResult{}, fmt.Errorf("result line must be a JSON object: %s", trimmed)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop map[string]json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return caseResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return caseResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}

	rawID, hasID := rawTop["id"]
	rawStepsField, hasSteps := rawTop["steps"]
	if len(rawTop) != 2 || !hasID || !hasSteps {
		return caseResult{}, fmt.Errorf("result object must have exact keys id, steps: %s", trimmed)
	}

	id, err := decodeNonNegInt(rawID)
	if err != nil {
		return caseResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var rawSteps []json.RawMessage
	if err := decodeStrictJSON(rawStepsField, &rawSteps); err != nil {
		return caseResult{}, fmt.Errorf("invalid steps: %w", err)
	}

	steps := make([]step, len(rawSteps))
	for i, rs := range rawSteps {
		var rawStep map[string]json.RawMessage
		if err := decodeStrictJSON(rs, &rawStep); err != nil {
			return caseResult{}, fmt.Errorf("step[%d]: %w", i, err)
		}
		rawOK, hasOK := rawStep["ok"]
		rawVal, hasVal := rawStep["value"]
		rawRev, hasRev := rawStep["revision"]
		if len(rawStep) != 3 || !hasOK || !hasVal || !hasRev {
			return caseResult{}, fmt.Errorf("step[%d] object must have exact keys ok, value, revision", i)
		}

		var ok bool
		if err := decodeStrictJSON(rawOK, &ok); err != nil {
			return caseResult{}, fmt.Errorf("step[%d].ok: %w", i, err)
		}
		var val *int
		if !isJSONNull(rawVal) {
			n, err := decodeNonNegInt(rawVal)
			if err != nil {
				return caseResult{}, fmt.Errorf("step[%d].value: %w", i, err)
			}
			val = &n
		}
		rev, err := decodeNonNegInt(rawRev)
		if err != nil {
			return caseResult{}, fmt.Errorf("step[%d].revision: %w", i, err)
		}
		steps[i] = step{
			OK:       ok,
			Value:    val,
			Revision: rev,
		}
	}

	return caseResult{
		ID:    id,
		Steps: steps,
	}, nil
}

func compareCaseResult(want, got caseResult) string {
	if want.ID != got.ID {
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	}
	if len(want.Steps) != len(got.Steps) {
		return fmt.Sprintf("steps len mismatch: lean=%d go=%d", len(want.Steps), len(got.Steps))
	}
	for i := range want.Steps {
		w := want.Steps[i]
		g := got.Steps[i]
		if w.OK != g.OK {
			return fmt.Sprintf("step[%d].ok mismatch: lean=%v go=%v", i, w.OK, g.OK)
		}
		if (w.Value == nil) != (g.Value == nil) || (w.Value != nil && *w.Value != *g.Value) {
			return fmt.Sprintf("step[%d].value mismatch: lean=%s go=%s", i, formatIntPtr(w.Value), formatIntPtr(g.Value))
		}
		if w.Revision != g.Revision {
			return fmt.Sprintf("step[%d].revision mismatch: lean=%d go=%d", i, w.Revision, g.Revision)
		}
	}
	return ""
}

func formatIntPtr(p *int) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *p)
}

func cloneResult(r caseResult) caseResult {
	steps := make([]step, len(r.Steps))
	for i, s := range r.Steps {
		var val *int
		if s.Value != nil {
			v := *s.Value
			val = &v
		}
		steps[i] = step{
			OK:       s.OK,
			Value:    val,
			Revision: s.Revision,
		}
	}
	return caseResult{
		ID:    r.ID,
		Steps: steps,
	}
}

func TestRegisterMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	cases := generateCases(cfg.Seed, cfg.Cases)
	encoded := encodeCases(t, cases)
	leanLines, err := RunModel("register", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(leanLines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(leanLines), len(cases))
	}

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt ./core/drt/...", cfg.Seed, cfg.Cases)
	totalSteps := 0
	for i, tc := range cases {
		leanRes, err := decodeResult(leanLines[i])
		if err != nil {
			t.Fatalf("seed=%d id=%d decode error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		goRes := runRegister(tc, stepRegister)
		if diff := compareCaseResult(leanRes, goRes); diff != "" {
			goJSON, marshalErr := json.Marshal(goRes)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], goJSON, repro)
		}
		totalSteps += len(tc.Ops)
	}
	if totalSteps == 0 {
		t.Fatal("zero ops exercised across all cases")
	}
	t.Logf("compared %d cases and %d steps (seed=%d)", len(cases), totalSteps, cfg.Seed)
}

func TestRegisterComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateCases(1, 50)
	leanLines, err := RunModel("register", encodeCases(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	if len(leanLines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(leanLines), len(cases))
	}

	applied := map[string]int{
		"id+1":                       0,
		"drop-last-step":             0,
		"append-duplicate-last-step": 0,
		"flip-ok":                    0,
		"value-null-to-0":            0,
		"value-num-to-null":          0,
		"value-num-to-num+1":         0,
		"revision+1":                 0,
	}

	assertDetects := func(kind string, orig, mut caseResult) {
		t.Helper()
		applied[kind]++
		if compareCaseResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, line := range leanLines {
		orig, err := decodeResult(line)
		if err != nil {
			t.Fatal(err)
		}

		mutID := cloneResult(orig)
		mutID.ID++
		assertDetects("id+1", orig, mutID)

		if len(orig.Steps) > 0 {
			mutDrop := cloneResult(orig)
			mutDrop.Steps = mutDrop.Steps[:len(mutDrop.Steps)-1]
			assertDetects("drop-last-step", orig, mutDrop)

			mutDup := cloneResult(orig)
			last := mutDup.Steps[len(mutDup.Steps)-1]
			if last.Value != nil {
				v := *last.Value
				last.Value = &v
			}
			mutDup.Steps = append(mutDup.Steps, last)
			assertDetects("append-duplicate-last-step", orig, mutDup)
		}

		for j, s := range orig.Steps {
			mutOK := cloneResult(orig)
			mutOK.Steps[j].OK = !mutOK.Steps[j].OK
			assertDetects("flip-ok", orig, mutOK)

			mutRev := cloneResult(orig)
			mutRev.Steps[j].Revision++
			assertDetects("revision+1", orig, mutRev)

			if s.Value == nil {
				mutVal := cloneResult(orig)
				zero := 0
				mutVal.Steps[j].Value = &zero
				assertDetects("value-null-to-0", orig, mutVal)
			} else {
				mutNull := cloneResult(orig)
				mutNull.Steps[j].Value = nil
				assertDetects("value-num-to-null", orig, mutNull)

				mutInc := cloneResult(orig)
				next := *s.Value + 1
				mutInc.Steps[j].Value = &next
				assertDetects("value-num-to-num+1", orig, mutInc)
			}
		}
	}

	for kind, count := range applied {
		if count == 0 {
			t.Errorf("mutation %q was never applicable across 50 cases", kind)
		}
	}

	var raw map[string]any
	if err := json.Unmarshal(leanLines[0], &raw); err != nil {
		t.Fatal(err)
	}
	raw["extra"] = 1
	withTopExtra, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	invalidLines := []struct {
		name string
		line []byte
	}{
		{"unknown top-level field", withTopExtra},
		{"unknown step field", []byte(`{"id":0,"steps":[{"ok":true,"value":1,"revision":1,"extra":1}]}`)},
		{"empty step object", []byte(`{"id":0,"steps":[{}]}`)},
		{"missing id", []byte(`{"steps":[]}`)},
		{"missing steps", []byte(`{"id":0}`)},
		{"null steps", []byte(`{"id":0,"steps":null}`)},
		{"null id", []byte(`{"id":null,"steps":[]}`)},
		{"null ok", []byte(`{"id":0,"steps":[{"ok":null,"value":null,"revision":0}]}`)},
		{"null revision", []byte(`{"id":0,"steps":[{"ok":true,"value":null,"revision":null}]}`)},
		{"missing value", []byte(`{"id":0,"steps":[{"ok":true,"revision":1}]}`)},
		{"uppercase top-level keys", []byte(`{"ID":0,"Steps":[]}`)},
		{"uppercase step keys", []byte(`{"id":0,"steps":[{"OK":false,"Value":null,"Revision":0}]}`)},
		{"float value", []byte(`{"id":0,"steps":[{"ok":true,"value":1.5,"revision":1}]}`)},
		{"negative revision", []byte(`{"id":0,"steps":[{"ok":true,"value":1,"revision":-1}]}`)},
		{"trailing object", []byte(`{"id":0,"steps":[]} {"id":1,"steps":[]}`)},
	}
	for _, tc := range invalidLines {
		if _, err := decodeResult(tc.line); err == nil {
			t.Errorf("decodeResult accepted %s: %s", tc.name, tc.line)
		}
	}

	v2 := 2
	validLines := []struct {
		line string
		want caseResult
	}{
		{
			line: `{"id":0,"steps":[]}`,
			want: caseResult{ID: 0, Steps: []step{}},
		},
		{
			line: `{"id":3,"steps":[{"ok":true,"value":2,"revision":1},{"ok":false,"value":null,"revision":1}]}`,
			want: caseResult{
				ID: 3,
				Steps: []step{
					{OK: true, Value: &v2, Revision: 1},
					{OK: false, Value: nil, Revision: 1},
				},
			},
		},
	}
	for _, tc := range validLines {
		got, err := decodeResult([]byte(tc.line))
		if err != nil {
			t.Fatalf("decodeResult(%s) unexpected error: %v", tc.line, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("decodeResult(%s) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
}

func TestRegisterDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateCases(1, 1000)
	leanLines, err := RunModel("register", encodeCases(t, cases))
	if err != nil {
		t.Fatal(err)
	}
	if len(leanLines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(leanLines), len(cases))
	}

	leanResults := make([]caseResult, len(cases))
	for i, line := range leanLines {
		res, err := decodeResult(line)
		if err != nil {
			t.Fatal(err)
		}
		leanResults[i] = res
	}

	variants := []struct {
		name string
		fn   stepFunc
	}{
		{
			name: "delete on absent value bumps revision and returns ok",
			fn: func(state register, o op) (register, bool) {
				if o.Op == "delete" {
					state.hasValue = false
					state.value = 0
					state.revision++
					return state, true
				}
				return stepRegister(state, o)
			},
		},
		{
			name: "cas ignores expect and always succeeds",
			fn: func(state register, o op) (register, bool) {
				if o.Op == "cas" {
					state.hasValue = true
					state.value = *o.Value
					state.revision++
					return state, true
				}
				return stepRegister(state, o)
			},
		},
		{
			name: "put does not bump revision",
			fn: func(state register, o op) (register, bool) {
				if o.Op == "put" {
					state.hasValue = true
					state.value = *o.Value
					return state, true
				}
				return stepRegister(state, o)
			},
		},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				if compareCaseResult(leanResults[i], runRegister(tc, v.fn)) != "" {
					mismatches++
				}
			}
			if mismatches == 0 {
				t.Fatalf("broken implementation %q had 0 mismatches against Lean model", v.name)
			}
		})
	}
}

func TestGenerateCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeCases(t, generateCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeCases(t, generateCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeCases(t, generateCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestRunModelRejectsUnknownSuite(t *testing.T) {
	_, err := RunModel("no-such-suite", [][]byte{[]byte(`{"id":0,"ops":[]}`)})
	if err == nil {
		t.Fatal("RunModel accepted unknown suite")
	}
	if !strings.Contains(err.Error(), "usage: nayutar-drt") {
		t.Fatalf("error %q does not contain %q", err.Error(), "usage: nayutar-drt")
	}
}

func TestRunModelRejectsMalformedCase(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"id":0,"ops":[{"op":"put","value":1}]}`),
		[]byte(`{"id":1,"ops":[{"op":"explode"}]}`),
	}
	_, err := RunModel("register", cases)
	if err == nil {
		t.Fatal("RunModel accepted malformed case")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error %q does not contain %q", err.Error(), "line 2")
	}
}
