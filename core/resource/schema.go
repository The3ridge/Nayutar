package resource

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// KindSchema is a compiled LKAL v0.2 schema for one resource kind, holding its
// spec completion/validation schema, status validation schema, and derived reference rules.
type KindSchema struct {
	raw          *schema.Schema
	specSchema   *schema.Schema
	statusSchema *schema.Schema
	refRules     []refs.Rule
}

// Schemas maps qualified kinds ("<kind>" for core, "<kind>.<group>" otherwise) to their KindSchema.
type Schemas map[string]*KindSchema

// Schema returns the underlying compiled LKAL v0.2 schema.
func (k *KindSchema) Schema() *schema.Schema {
	if k == nil {
		return nil
	}
	return k.raw
}

// RefRules returns a copy of the write-time reference rules derived from spec's .ref targets.
func (k *KindSchema) RefRules() []refs.Rule {
	if k == nil || len(k.refRules) == 0 {
		return nil
	}
	out := make([]refs.Rule, len(k.refRules))
	for i, r := range k.refRules {
		out[i] = refs.Rule{
			Path:    slices.Clone(r.Path),
			Targets: slices.Clone(r.Targets),
		}
	}
	return out
}

// CompileKindSchema parses and compiles an LKAL v0.2 resource kind schema document.
func CompileKindSchema(name string, src []byte) (*KindSchema, error) {
	f, err := lkal.Parse(name, src)
	if err != nil {
		return nil, err
	}
	return CompileKindSchemaFile(f)
}

// CompileKindSchemaFile compiles a parsed *lkal.File as a resource kind schema.
func CompileKindSchemaFile(f *lkal.File) (*KindSchema, error) {
	baseSchema, err := schema.CompileFile(f)
	if err != nil {
		return nil, err
	}

	data := f.Data()
	defs, defOrder := collectSchemaDefs(data)
	rootRec, _ := findRecordField(data, "root")

	if err := checkNoStructuralCycles(rootRec, defs, defOrder); err != nil {
		return nil, err
	}
	if boolFieldValue(rootRec, "open") {
		return nil, errors.New("resource: schema root cannot set open = true; top-level fields are fixed to meta, spec, and status")
	}

	rootFields, hasRootFields := findRecordField(rootRec, "field")
	var (
		hasSpec        bool
		specFieldRec   lkal.Record
		hasStatus      bool
		statusFieldRec lkal.Record
	)
	if hasRootFields {
		for _, fld := range rootFields.Fields {
			fRec := fld.Value.(lkal.Record)
			switch fld.Key {
			case "meta":
				if hasFieldKey(fRec, "default") {
					return nil, errors.New("resource: schema meta cannot declare default")
				}
				resolved := derefTypeSpec(fRec, defs)
				if enumFieldValue(resolved, "type") != "record" {
					return nil, errors.New("resource: schema meta must have type .record")
				}
				if err := checkDisallowedInSection(resolved, defs, "meta"); err != nil {
					return nil, err
				}
			case "spec":
				hasSpec = true
				specFieldRec = fRec
				resolved := derefTypeSpec(fRec, defs)
				tp := enumFieldValue(resolved, "type")
				if tp != "record" && tp != "map" {
					return nil, errors.New("resource: schema spec must have type .record or .map")
				}
			case "status":
				if hasFieldKey(fRec, "default") {
					return nil, errors.New("resource: schema status cannot declare default")
				}
				hasStatus = true
				statusFieldRec = fRec
				resolved := derefTypeSpec(fRec, defs)
				tp := enumFieldValue(resolved, "type")
				if tp != "record" && tp != "map" {
					return nil, errors.New("resource: schema status must have type .record or .map")
				}
				if err := checkDisallowedInSection(resolved, defs, "status"); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("resource: unexpected schema root field %q; only meta, spec, and status are allowed", fld.Key)
			}
		}
	}

	var rules []refs.Rule
	if hasSpec {
		var err error
		rules, err = extractSpecRefRules(derefTypeSpec(specFieldRec, defs), defs, []string{"spec"})
		if err != nil {
			return nil, err
		}
	}

	specSchema, err := buildSpecSubschema(data, hasSpec, specFieldRec)
	if err != nil {
		return nil, err
	}
	var statusSchema *schema.Schema
	if hasStatus {
		statusSchema, err = buildStatusSubschema(data, defs, statusFieldRec)
		if err != nil {
			return nil, err
		}
	}

	return &KindSchema{
		raw:          baseSchema,
		specSchema:   specSchema,
		statusSchema: statusSchema,
		refRules:     rules,
	}, nil
}

func validateSchemasMap(s Schemas) (refs.Schema, error) {
	if len(s) == 0 {
		return refs.Schema{}, nil
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	derived := make(refs.Schema, len(s))
	for _, qkind := range keys {
		if _, _, err := keyspace.ParseQualifiedKind(qkind); err != nil {
			return nil, fmt.Errorf("refs: invalid schema kind %q: %w", qkind, err)
		}
		ks := s[qkind]
		if ks == nil || ks.specSchema == nil {
			return nil, fmt.Errorf("refs: invalid schema for kind %q: nil KindSchema", qkind)
		}
		derived[qkind] = ks.RefRules()
	}
	if err := derived.Validate(); err != nil {
		return nil, err
	}
	return derived, nil
}

func (k *KindSchema) completeCandidate(cand lkal.Record) (lkal.Record, error) {
	var wrapper lkal.Record
	if specVal, ok := findValue(cand, "spec"); ok {
		wrapper = lkal.Record{Fields: []lkal.Field{{Key: "spec", Value: specVal}}}
	}
	completedVal, err := k.specSchema.Complete(wrapper)
	if err != nil {
		return lkal.Record{}, toMalformedEnvelopeErr(err)
	}
	completedWrapper := completedVal.(lkal.Record)
	completedSpec, hasCompletedSpec := findValue(completedWrapper, "spec")

	outFields := make([]lkal.Field, 0, len(cand.Fields)+1)
	replacedSpec := false
	for _, fld := range cand.Fields {
		if fld.Key == "spec" {
			if hasCompletedSpec {
				outFields = append(outFields, lkal.Field{Key: "spec", Value: completedSpec})
				replacedSpec = true
			}
			continue
		}
		outFields = append(outFields, fld)
	}
	if hasCompletedSpec && !replacedSpec {
		outFields = append(outFields, lkal.Field{Key: "spec", Value: completedSpec})
	}
	return lkal.Record{Fields: outFields}, nil
}

func (k *KindSchema) validateSpec(doc lkal.Record) error {
	var wrapper lkal.Record
	if specVal, ok := findValue(doc, "spec"); ok {
		wrapper = lkal.Record{Fields: []lkal.Field{{Key: "spec", Value: specVal}}}
	}
	if err := k.specSchema.ValidateValue(wrapper); err != nil {
		return toMalformedEnvelopeErr(err)
	}
	return nil
}

func (k *KindSchema) validateStatus(doc lkal.Record) error {
	if k.statusSchema == nil {
		return nil
	}
	var wrapper lkal.Record
	if statusVal, ok := findValue(doc, "status"); ok {
		wrapper = lkal.Record{Fields: []lkal.Field{{Key: "status", Value: statusVal}}}
	}
	if err := k.statusSchema.ValidateValue(wrapper); err != nil {
		return toMalformedEnvelopeErr(err)
	}
	return nil
}

func toMalformedEnvelopeErr(err error) error {
	var el *lkal.ErrorList
	if errors.As(err, &el) && len(el.Errors) > 0 {
		first := el.Errors[0]
		detail := first.Code + " " + first.Msg
		if first.Hint != "" {
			detail += ": " + first.Hint
		}
		return &envelope.Error{
			Kind:   envelope.ErrMalformed,
			Path:   first.Path,
			Detail: detail,
		}
	}
	return &envelope.Error{Kind: envelope.ErrMalformed, Detail: err.Error()}
}

func collectSchemaDefs(data lkal.Record) (map[string]lkal.Record, []string) {
	defs := make(map[string]lkal.Record)
	var order []string
	if defRec, ok := findRecordField(data, "def"); ok {
		for _, fld := range defRec.Fields {
			if r, isRec := fld.Value.(lkal.Record); isRec {
				defs[fld.Key] = r
				order = append(order, fld.Key)
			}
		}
	}
	return defs, order
}

func derefTypeSpec(rec lkal.Record, defs map[string]lkal.Record) lkal.Record {
	cur := rec
	for {
		ref, ok := useDefName(cur)
		if !ok {
			return cur
		}
		next, exists := defs[ref]
		if !exists {
			return cur
		}
		cur = next
	}
}

func useDefName(rec lkal.Record) (string, bool) {
	v, ok := findValue(rec, "use")
	if !ok {
		return "", false
	}
	r, ok := v.(lkal.Ref)
	if !ok || len(r) != 2 || r[0] != "def" {
		return "", false
	}
	return r[1], true
}

func checkNoStructuralCycles(rootRec lkal.Record, defs map[string]lkal.Record, defOrder []string) error {
	active := make(map[string]bool, len(defs))
	visited := make(map[string]bool, len(defs))
	var stack []string

	var walkSpec func(rec lkal.Record) error
	walkDef := func(name string) error {
		if active[name] {
			idx := slices.Index(stack, name)
			cycle := append(slices.Clone(stack[idx:]), name)
			return fmt.Errorf("resource: recursive schema definition %s is not supported", strings.Join(cycle, " -> "))
		}
		if visited[name] {
			return nil
		}
		active[name] = true
		stack = append(stack, name)
		err := walkSpec(defs[name])
		stack = stack[:len(stack)-1]
		active[name] = false
		visited[name] = true
		return err
	}

	walkSpec = func(rec lkal.Record) error {
		if target, ok := useDefName(rec); ok {
			return walkDef(target)
		}
		if typeAllows(rec, "record") {
			if fieldsRec, ok := findRecordField(rec, "field"); ok {
				for _, fld := range fieldsRec.Fields {
					if err := walkSpec(fld.Value.(lkal.Record)); err != nil {
						return err
					}
				}
			}
		}
		if typeAllows(rec, "map") {
			if valRec, ok := findRecordField(rec, "value"); ok {
				if err := walkSpec(valRec); err != nil {
					return err
				}
			}
		}
		if typeAllows(rec, "list") {
			if itemsRec, ok := findRecordField(rec, "items"); ok {
				if err := walkSpec(itemsRec); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if fieldsRec, ok := findRecordField(rootRec, "field"); ok {
		for _, fld := range fieldsRec.Fields {
			if err := walkSpec(fld.Value.(lkal.Record)); err != nil {
				return err
			}
		}
	}
	for _, name := range defOrder {
		if err := walkDef(name); err != nil {
			return err
		}
	}
	return nil
}

func checkDisallowedInSection(rec lkal.Record, defs map[string]lkal.Record, path string) error {
	resolved := derefTypeSpec(rec, defs)
	if typeAllows(resolved, "ref") {
		return fmt.Errorf("resource: %s: .ref type is only allowed under spec", path)
	}
	if typeAllows(resolved, "record") {
		if fieldsRec, ok := findRecordField(resolved, "field"); ok {
			for _, fld := range fieldsRec.Fields {
				fRec := fld.Value.(lkal.Record)
				childPath := path + "/" + fld.Key
				if hasFieldKey(fRec, "default") {
					return fmt.Errorf("resource: %s: default is only allowed under spec", childPath)
				}
				if err := checkDisallowedInSection(fRec, defs, childPath); err != nil {
					return err
				}
			}
		}
	}
	if typeAllows(resolved, "map") {
		if valRec, ok := findRecordField(resolved, "value"); ok {
			if err := checkDisallowedInSection(valRec, defs, path+"/*"); err != nil {
				return err
			}
		}
	}
	if typeAllows(resolved, "list") {
		if itemsRec, ok := findRecordField(resolved, "items"); ok {
			if err := checkDisallowedInSection(itemsRec, defs, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractSpecRefRules(rec lkal.Record, defs map[string]lkal.Record, path []string) ([]refs.Rule, error) {
	resolved := derefTypeSpec(rec, defs)
	var out []refs.Rule

	if typeAllows(resolved, "ref") {
		if len(path) < 2 {
			return nil, errors.New("resource: spec itself cannot be a .ref")
		}
		targetsVal, ok := findValue(resolved, "targets")
		if !ok {
			return nil, fmt.Errorf("resource: %s: .ref field must declare targets", strings.Join(path, "/"))
		}
		targetsList, ok := targetsVal.(lkal.List)
		if !ok || len(targetsList) == 0 {
			return nil, fmt.Errorf("resource: %s: .ref field must declare non-empty targets", strings.Join(path, "/"))
		}
		targets := make([]string, 0, len(targetsList))
		seen := make(map[string]bool, len(targetsList))
		for _, el := range targetsList {
			r, ok := el.(lkal.Ref)
			if !ok || len(r) != 1 {
				return nil, fmt.Errorf("resource: %s: target must be a single-segment qualified kind reference", strings.Join(path, "/"))
			}
			if _, _, err := keyspace.ParseQualifiedKind(r[0]); err != nil {
				return nil, fmt.Errorf("resource: %s: invalid target kind %q: %w", strings.Join(path, "/"), r[0], err)
			}
			if !seen[r[0]] {
				seen[r[0]] = true
				targets = append(targets, r[0])
			}
		}
		out = append(out, refs.Rule{
			Path:    slices.Clone(path),
			Targets: targets,
		})
	}

	if typeAllows(resolved, "record") {
		if fieldsRec, ok := findRecordField(resolved, "field"); ok {
			for _, fld := range fieldsRec.Fields {
				childPath := append(slices.Clone(path), fld.Key)
				subRules, err := extractSpecRefRules(fld.Value.(lkal.Record), defs, childPath)
				if err != nil {
					return nil, err
				}
				if len(subRules) > 0 {
					if fld.Key == "" || strings.Contains(fld.Key, "/") || fld.Key == "*" {
						return nil, fmt.Errorf("resource: invalid field key %q on reference path %s", fld.Key, strings.Join(childPath, "/"))
					}
					out = append(out, subRules...)
				}
			}
		}
	}
	if typeAllows(resolved, "map") {
		if valRec, ok := findRecordField(resolved, "value"); ok {
			childPath := append(slices.Clone(path), "*")
			subRules, err := extractSpecRefRules(valRec, defs, childPath)
			if err != nil {
				return nil, err
			}
			out = append(out, subRules...)
		}
	}
	if typeAllows(resolved, "list") {
		if itemsRec, ok := findRecordField(resolved, "items"); ok {
			subRules, err := extractSpecRefRules(itemsRec, defs, path)
			if err != nil {
				return nil, err
			}
			out = append(out, subRules...)
		}
	}
	return out, nil
}

func typeAllows(rec lkal.Record, want string) bool {
	if enumFieldValue(rec, "type") == want {
		return true
	}
	if typesVal, ok := findValue(rec, "types"); ok {
		if lst, isList := typesVal.(lkal.List); isList {
			for _, el := range lst {
				if e, isEnum := el.(lkal.Enum); isEnum && string(e) == want {
					return true
				}
			}
		}
	}
	return false
}

// buildSpecSubschema compiles an internal schema rooted at { field "spec" { ... } } with .ref
// `targets` stripped so schema validation never emits L508 (leaving reference target-kind checking
// to refs.Prepare phase 5, where only added occurrences are checked and L508 cannot consume the 20-error cap).
func buildSpecSubschema(data lkal.Record, hasSpec bool, specFieldRec lkal.Record) (*schema.Schema, error) {
	var root lkal.Record
	if hasSpec {
		root = lkal.Record{
			Fields: []lkal.Field{
				{Key: "field", Value: lkal.Record{
					Fields: []lkal.Field{
						{Key: "spec", Value: stripRefTargets(specFieldRec)},
					},
				}},
			},
		}
	}
	docFields := []lkal.Field{
		{Key: "lkal-schema", Value: lkal.String("0.2")},
	}
	if defRec, ok := findRecordField(data, "def"); ok {
		strippedDefs := make([]lkal.Field, len(defRec.Fields))
		for i, fld := range defRec.Fields {
			strippedDefs[i] = lkal.Field{Key: fld.Key, Value: stripRefTargets(fld.Value.(lkal.Record))}
		}
		docFields = append(docFields, lkal.Field{Key: "def", Value: lkal.Record{Fields: strippedDefs}})
	}
	docFields = append(docFields, lkal.Field{Key: "root", Value: root})
	return compileSchemaRecord("spec.schema.lkal", lkal.Record{Fields: docFields})
}

func stripRefTargets(rec lkal.Record) lkal.Record {
	isRef := typeAllows(rec, "ref")
	isRec := typeAllows(rec, "record")
	isMap := typeAllows(rec, "map")
	isList := typeAllows(rec, "list")

	out := make([]lkal.Field, 0, len(rec.Fields))
	for _, fld := range rec.Fields {
		if isRef && fld.Key == "targets" {
			continue
		}
		switch {
		case isRec && fld.Key == "field":
			if fMap, ok := fld.Value.(lkal.Record); ok {
				sub := make([]lkal.Field, len(fMap.Fields))
				for i, sf := range fMap.Fields {
					sub[i] = lkal.Field{Key: sf.Key, Value: stripRefTargets(sf.Value.(lkal.Record))}
				}
				out = append(out, lkal.Field{Key: fld.Key, Value: lkal.Record{Fields: sub}})
				continue
			}
		case isMap && fld.Key == "value":
			if valRec, ok := fld.Value.(lkal.Record); ok {
				out = append(out, lkal.Field{Key: fld.Key, Value: stripRefTargets(valRec)})
				continue
			}
		case isList && fld.Key == "items":
			if itemRec, ok := fld.Value.(lkal.Record); ok {
				out = append(out, lkal.Field{Key: fld.Key, Value: stripRefTargets(itemRec)})
				continue
			}
		}
		out = append(out, fld)
	}
	return lkal.Record{Fields: out}
}

func buildStatusSubschema(data lkal.Record, defs map[string]lkal.Record, statusFieldRec lkal.Record) (*schema.Schema, error) {
	effectiveStatus := augmentClosedStatus(statusFieldRec, defs)
	docFields := []lkal.Field{
		{Key: "lkal-schema", Value: lkal.String("0.2")},
	}
	if defRec, ok := findRecordField(data, "def"); ok {
		docFields = append(docFields, lkal.Field{Key: "def", Value: defRec})
	}
	docFields = append(docFields, lkal.Field{
		Key: "root",
		Value: lkal.Record{
			Fields: []lkal.Field{
				{Key: "field", Value: lkal.Record{
					Fields: []lkal.Field{
						{Key: "status", Value: effectiveStatus},
					},
				}},
			},
		},
	})
	return compileSchemaRecord("status.schema.lkal", lkal.Record{Fields: docFields})
}

func augmentClosedStatus(statusFieldRec lkal.Record, defs map[string]lkal.Record) lkal.Record {
	resolved := derefTypeSpec(statusFieldRec, defs)
	if enumFieldValue(resolved, "type") != "record" || boolFieldValue(resolved, "open") {
		return statusFieldRec
	}

	reserved := stringListSet(resolved, "reserved")
	existingFields, _ := findRecordField(resolved, "field")
	needObs := !hasFieldKey(existingFields, "observedGeneration") && !reserved["observedGeneration"]
	needCond := !hasFieldKey(existingFields, "condition") && !reserved["condition"]
	if !needObs && !needCond {
		return statusFieldRec
	}

	mergedFields := slices.Clone(existingFields.Fields)
	if needObs {
		mergedFields = append(mergedFields, lkal.Field{
			Key: "observedGeneration",
			Value: lkal.Record{Fields: []lkal.Field{
				{Key: "type", Value: lkal.Enum("int")},
				{Key: "min", Value: lkal.Int(1)},
			}},
		})
	}
	if needCond {
		mergedFields = append(mergedFields, lkal.Field{
			Key:   "condition",
			Value: envelopeConditionFieldSpec(),
		})
	}

	out := make([]lkal.Field, 0, len(resolved.Fields)+2)
	if reqVal, ok := findValue(statusFieldRec, "required"); ok {
		out = append(out, lkal.Field{Key: "required", Value: reqVal})
	}
	wroteField := false
	for _, fld := range resolved.Fields {
		if fld.Key == "required" {
			continue
		}
		if fld.Key == "field" {
			out = append(out, lkal.Field{Key: "field", Value: lkal.Record{Fields: mergedFields}})
			wroteField = true
			continue
		}
		out = append(out, fld)
	}
	if !wroteField {
		out = append(out, lkal.Field{Key: "field", Value: lkal.Record{Fields: mergedFields}})
	}
	return lkal.Record{Fields: out}
}

func envelopeConditionFieldSpec() lkal.Record {
	return lkal.Record{Fields: []lkal.Field{
		{Key: "type", Value: lkal.Enum("map")},
		{Key: "key", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "min-length", Value: lkal.Int(1)},
			{Key: "max-length", Value: lkal.Int(63)},
			{Key: "pattern", Value: lkal.String("[A-Z][A-Za-z0-9]*")},
		}}},
		{Key: "value", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "type", Value: lkal.Enum("record")},
			{Key: "field", Value: lkal.Record{Fields: []lkal.Field{
				{Key: "state", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "type", Value: lkal.Enum("enum")},
					{Key: "required", Value: lkal.Bool(true)},
					{Key: "values", Value: lkal.List{lkal.Enum("true"), lkal.Enum("false"), lkal.Enum("unknown")}},
				}}},
				{Key: "reason", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "type", Value: lkal.Enum("string")},
					{Key: "required", Value: lkal.Bool(true)},
					{Key: "min-length", Value: lkal.Int(1)},
					{Key: "max-length", Value: lkal.Int(1024)},
					{Key: "pattern", Value: lkal.String("[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?")},
				}}},
				{Key: "since", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "type", Value: lkal.Enum("string")},
					{Key: "pattern", Value: lkal.String("[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z")},
				}}},
			}}},
		}}},
	}}
}

func compileSchemaRecord(name string, rec lkal.Record) (*schema.Schema, error) {
	jf, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, err
	}
	for _, fld := range rec.Fields {
		if err := jf.Set(fld.Key, fld.Value); err != nil {
			return nil, err
		}
	}
	return schema.Compile(name, jf.Format())
}

func findValue(rec lkal.Record, key string) (lkal.Value, bool) {
	for _, fld := range rec.Fields {
		if fld.Key == key {
			return fld.Value, true
		}
	}
	return nil, false
}

func hasFieldKey(rec lkal.Record, key string) bool {
	_, ok := findValue(rec, key)
	return ok
}

func findRecordField(rec lkal.Record, key string) (lkal.Record, bool) {
	v, ok := findValue(rec, key)
	if !ok {
		return lkal.Record{}, false
	}
	r, ok := v.(lkal.Record)
	return r, ok
}

func enumFieldValue(rec lkal.Record, key string) string {
	v, ok := findValue(rec, key)
	if !ok {
		return ""
	}
	e, ok := v.(lkal.Enum)
	if !ok {
		return ""
	}
	return string(e)
}

func boolFieldValue(rec lkal.Record, key string) bool {
	v, ok := findValue(rec, key)
	if !ok {
		return false
	}
	b, ok := v.(lkal.Bool)
	return ok && bool(b)
}

func stringListSet(rec lkal.Record, key string) map[string]bool {
	v, ok := findValue(rec, key)
	if !ok {
		return nil
	}
	lst, ok := v.(lkal.List)
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(lst))
	for _, el := range lst {
		if s, isStr := el.(lkal.String); isStr {
			out[string(s)] = true
		}
	}
	return out
}
