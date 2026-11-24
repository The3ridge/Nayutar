package ownership

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	pathTokenPrefix = "ksg-tok-v1:"
	docTokenPrefix  = "ksg-doc-v1:"
)

// PathConditionToken computes the deterministic path token "ksg-tok-v1:<sha256hex>"
// binding object identity, path, ancestor kinds, and observed value.
func PathConditionToken(obj keyspace.Object, p Path, doc lkal.Record) (string, error) {
	if err := validateObjectIdentity(obj); err != nil {
		return "", err
	}
	if err := ValidateEntranceSpecPath(p); err != nil {
		return "", err
	}
	obs, ancs := InspectDocPath(doc, p)
	return pathTokenFromObserved(obj, p, obs, ancs)
}

func pathTokenFromObserved(obj keyspace.Object, p Path, obs Observed, ancs []AncestorObs) (string, error) {
	for _, a := range ancs {
		if a.Observed.Kind == ObsUnknown {
			return "", newError(CodeObservationUnavailable, p, fmt.Sprintf("ancestor %s observation is unknown", a.Path.String()), "retry once observation is available")
		}
	}
	if obs.Kind == ObsUnknown {
		return "", newError(CodeObservationUnavailable, p, fmt.Sprintf("target %s observation is unknown", p.String()), "retry once observation is available")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "group=%s\nkind=%s\nname=%s\npath=%s\n", obj.Group, obj.Kind, obj.Name, p.String())
	for _, a := range ancs {
		s, err := serializeObservedToken(a.Observed)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "anc=%s:%s\n", a.Path.String(), s)
	}
	ts, err := serializeObservedToken(obs)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "target=%s:%s\n", p.String(), ts)

	sum := sha256.Sum256([]byte(b.String()))
	return pathTokenPrefix + hex.EncodeToString(sum[:]), nil
}

// VerifyPathConditionToken compares expected against the current path token for (obj, p, doc).
func VerifyPathConditionToken(expected string, obj keyspace.Object, p Path, doc lkal.Record) error {
	if strings.TrimSpace(expected) == "" {
		return newError(CodeInvalidInput, p, "condition token (expect) must not be empty", "")
	}
	actual, err := PathConditionToken(obj, p, doc)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return newError(CodeExternalDrift, p, fmt.Sprintf("condition token (expect) mismatch for %s", p.String()), "run explain to obtain a fresh expect token")
	}
	return nil
}

// DocConditionToken computes the deterministic document token "ksg-doc-v1:<sha256hex>"
// binding object identity and canonical EntranceSpec (meta/labels + spec).
func DocConditionToken(obj keyspace.Object, doc lkal.Record) (string, error) {
	if err := validateObjectIdentity(obj); err != nil {
		return "", err
	}
	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return "", newError(CodeInvalidInput, nil, err.Error(), "")
	}
	if metaVal, ok := findField(doc, "meta"); ok {
		if metaRec, isRec := metaVal.(lkal.Record); isRec {
			if labelsVal, hasLabels := findField(metaRec, "labels"); hasLabels {
				if labelsRec, isLRec := labelsVal.(lkal.Record); isLRec {
					if err := f.Set("labels", sortRecordKeys(labelsRec)); err != nil {
						return "", newError(CodeInvalidInput, Path{"meta", "labels"}, err.Error(), "")
					}
				}
			}
		}
	}
	if specVal, ok := findField(doc, "spec"); ok {
		if specRec, isRec := specVal.(lkal.Record); isRec {
			if err := f.Set("spec", sortRecordKeys(specRec)); err != nil {
				return "", newError(CodeInvalidInput, Path{"spec"}, err.Error(), "")
			}
		}
	}
	raw, err := lkal.ToJSON(f, false)
	if err != nil {
		return "", newError(CodeInvalidInput, nil, err.Error(), "")
	}
	j := jsontext.Value(raw)
	if err := j.Compact(); err != nil {
		return "", newError(CodeInvalidInput, nil, err.Error(), "")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "group=%s\nkind=%s\nname=%s\ndoc=%s\n", obj.Group, obj.Kind, obj.Name, string(j))
	sum := sha256.Sum256([]byte(b.String()))
	return docTokenPrefix + hex.EncodeToString(sum[:]), nil
}

// VerifyDocConditionToken compares expected against the current document token for (obj, doc).
func VerifyDocConditionToken(expected string, obj keyspace.Object, doc lkal.Record) error {
	if strings.TrimSpace(expected) == "" {
		return newError(CodeInvalidInput, nil, "condition token (expect) must not be empty", "")
	}
	actual, err := DocConditionToken(obj, doc)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return newError(CodeExternalDrift, nil, "document condition token (expect) mismatch", "run explain to obtain a fresh expect token")
	}
	return nil
}

func serializeObservedToken(obs Observed) (string, error) {
	switch obs.Kind {
	case ObsAbsent:
		return "Absent", nil
	case ObsRecord:
		return "Present(Record)", nil
	case ObsOpaque:
		return fmt.Sprintf("Present(Opaque:%q)", obs.Reason), nil
	case ObsLeaf:
		vJSON, err := canonicalValueJSON(obs.Value)
		if err != nil {
			return "", newError(CodeInvalidInput, nil, err.Error(), "")
		}
		return "Present(Leaf:" + vJSON + ")", nil
	default:
		return fmt.Sprintf("Unknown(%q)", obs.Reason), nil
	}
}

func validateObjectIdentity(obj keyspace.Object) error {
	if err := keyspace.ValidateGroup(obj.Group); err != nil {
		return newError(CodeInvalidInput, Path{"meta", "group"}, err.Error(), "")
	}
	if err := keyspace.ValidateKind(obj.Kind); err != nil {
		return newError(CodeInvalidInput, Path{"meta", "kind"}, err.Error(), "")
	}
	if err := keyspace.ValidateName(obj.Name); err != nil {
		return newError(CodeInvalidInput, Path{"meta", "name"}, err.Error(), "")
	}
	return nil
}
