package netpol

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
	"github.com/Kaikei-e/Nayutar/core/resource"
)

const (
	// ResourceGroup is the API group for networkpolicy resources.
	ResourceGroup = "net.nayutar"
	// ResourceKind is the resource kind name for networkpolicy resources.
	ResourceKind = "networkpolicy"
	// QualifiedResourceKind is the qualified kind identifier registered in resource.Schemas.
	QualifiedResourceKind = "networkpolicy.net.nayutar"
)

//go:embed netpol.schema.lkal
var bundleSchemaBytes []byte

//go:embed kind.schema.lkal
var kindSchemaBytes []byte

var (
	compiledBundleSchema *schema.Schema
	compiledKindSchema   *resource.KindSchema
)

func init() {
	s, err := schema.Compile("netpol.schema.lkal", bundleSchemaBytes)
	if err != nil {
		panic("netpol: embedded netpol.schema.lkal failed to compile: " + err.Error())
	}
	compiledBundleSchema = s

	ks, err := resource.CompileKindSchema("kind.schema.lkal", kindSchemaBytes)
	if err != nil {
		panic("netpol: embedded kind.schema.lkal failed to compile: " + err.Error())
	}
	compiledKindSchema = ks
}

// Schema returns the compiled embedded LKAL v0.2 schema for standalone PolicyBundle files.
func Schema() *schema.Schema {
	return compiledBundleSchema
}

// KindSchema returns the compiled resource.KindSchema for net.nayutar/networkpolicy.
func KindSchema() (*resource.KindSchema, error) {
	if compiledKindSchema == nil {
		return resource.CompileKindSchema("kind.schema.lkal", kindSchemaBytes)
	}
	return compiledKindSchema, nil
}

// DecodeBundle parses, completes defaults, validates against Schema(), and decodes an LKAL PolicyBundle.
func DecodeBundle(name string, src []byte) (PolicyBundle, error) {
	f, err := lkal.Parse(name, src)
	if err != nil {
		return PolicyBundle{}, fmt.Errorf("parse %s: %w", name, err)
	}
	completed, err := compiledBundleSchema.Complete(f.Data())
	if err != nil {
		return PolicyBundle{}, fmt.Errorf("complete %s: %w", name, err)
	}
	if err := compiledBundleSchema.ValidateValue(completed); err != nil {
		return PolicyBundle{}, fmt.Errorf("validate %s: %w", name, err)
	}
	root := completed.(lkal.Record)
	tenantsRec, _ := recordField(root, "tenants")

	out := PolicyBundle{
		Tenants: make([]TenantPolicy, 0, len(tenantsRec.Fields)),
	}
	for _, tf := range tenantsRec.Fields {
		tRec := tf.Value.(lkal.Record)
		tp, err := decodeBundleTenant(tf.Key, tRec)
		if err != nil {
			return PolicyBundle{}, fmt.Errorf("decode %s: tenants.%s: %w", name, tf.Key, err)
		}
		out.Tenants = append(out.Tenants, tp)
	}
	slices.SortFunc(out.Tenants, func(a, b TenantPolicy) int {
		return strings.Compare(a.Tenant, b.Tenant)
	})
	if err := CheckDeployable(out.Tenants); err != nil {
		return PolicyBundle{}, fmt.Errorf("deploy check %s: %w", name, err)
	}
	return out, nil
}

// DecodeTenantPolicySpec decodes a validated networkpolicy spec record into a TenantPolicy,
// taking the tenant's CIDRs from the caller because the kind does not carry them.
func DecodeTenantPolicySpec(tenant string, spec lkal.Record, cidrs []Prefix) (TenantPolicy, error) {
	tp, err := decodeTenantPolicyBody(tenant, spec)
	if err != nil {
		return TenantPolicy{}, err
	}
	tp.CIDRs = slices.Clone(cidrs)
	return tp, nil
}

func decodeBundleTenant(tenant string, rec lkal.Record) (TenantPolicy, error) {
	tp, err := decodeTenantPolicyBody(tenant, rec)
	if err != nil {
		return TenantPolicy{}, err
	}
	cidrList, ok := listField(rec, "cidrs")
	if !ok || len(cidrList) == 0 {
		return TenantPolicy{}, fmt.Errorf("cidrs must be non-empty")
	}
	tp.CIDRs = make([]Prefix, 0, len(cidrList))
	for i, el := range cidrList {
		raw, isStr := el.(lkal.String)
		if !isStr {
			return TenantPolicy{}, fmt.Errorf("cidrs[%d] must be a string", i)
		}
		pfx, err := ParsePrefix(string(raw))
		if err != nil {
			return TenantPolicy{}, fmt.Errorf("cidrs[%d]: %w", i, err)
		}
		tp.CIDRs = append(tp.CIDRs, pfx)
	}
	return tp, nil
}

func decodeTenantPolicyBody(tenant string, rec lkal.Record) (TenantPolicy, error) {
	intra := IntraTenantMode(optionalEnumField(rec, "intraTenant"))
	if intra == "" {
		intra = IntraTenantAllow
	}
	if intra != IntraTenantAllow && intra != IntraTenantDeny {
		return TenantPolicy{}, fmt.Errorf("invalid intraTenant %q", intra)
	}

	var rules []Rule
	if rulesRec, hasRules := recordField(rec, "rules"); hasRules && len(rulesRec.Fields) > 0 {
		rules = make([]Rule, 0, len(rulesRec.Fields))
		for _, rf := range rulesRec.Fields {
			rRec, isRec := rf.Value.(lkal.Record)
			if !isRec {
				return TenantPolicy{}, fmt.Errorf("rules.%s must be a record", rf.Key)
			}
			r, err := decodeRuleRecord(rRec)
			if err != nil {
				return TenantPolicy{}, fmt.Errorf("rules.%s: %w", rf.Key, err)
			}
			rules = append(rules, r)
		}
		slices.SortFunc(rules, func(a, b Rule) int {
			if a.Order < b.Order {
				return -1
			}
			if a.Order > b.Order {
				return 1
			}
			return 0
		})
	}

	return TenantPolicy{
		Tenant:      tenant,
		IntraTenant: intra,
		Rules:       rules,
	}, nil
}

func decodeRuleRecord(rec lkal.Record) (Rule, error) {
	orderVal, hasOrder := intField(rec, "order")
	if !hasOrder {
		return Rule{}, fmt.Errorf("missing required field order")
	}
	dir := Direction(optionalEnumField(rec, "direction"))
	if dir != DirectionIngress && dir != DirectionEgress {
		return Rule{}, fmt.Errorf("invalid direction %q", dir)
	}
	proto := RuleProto(optionalEnumField(rec, "protocol"))
	if proto == "" {
		proto = RuleProtoAny
	}
	switch proto {
	case RuleProtoAny, RuleProtoTCP, RuleProtoUDP, RuleProtoICMP:
	default:
		return Rule{}, fmt.Errorf("invalid protocol %q", proto)
	}

	peerTenant, hasPeerTenant := stringField(rec, "peerTenant")
	peerCIDRStr, hasPeerCIDR := stringField(rec, "peerCIDR")
	if hasPeerTenant == hasPeerCIDR {
		return Rule{}, fmt.Errorf("rule must specify exactly one of peerTenant or peerCIDR")
	}

	r := Rule{
		Order:     orderVal,
		Direction: dir,
		Protocol:  proto,
	}
	if hasPeerTenant {
		r.PeerKind = PeerKindTenant
		r.PeerTenant = peerTenant
	} else {
		pfx, err := ParsePrefix(peerCIDRStr)
		if err != nil {
			return Rule{}, fmt.Errorf("peerCIDR: %w", err)
		}
		r.PeerKind = PeerKindCIDR
		r.PeerCIDR = pfx
	}

	if portsRec, hasPorts := recordField(rec, "ports"); hasPorts {
		pMin, okMin := intField(portsRec, "min")
		pMax, okMax := intField(portsRec, "max")
		if !okMin || !okMax {
			return Rule{}, fmt.Errorf("ports requires both min and max")
		}
		r.HasPorts = true
		r.PortMin = int(pMin)
		r.PortMax = int(pMax)
	}
	return r, nil
}

func recordField(rec lkal.Record, key string) (lkal.Record, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			r, ok := f.Value.(lkal.Record)
			return r, ok
		}
	}
	return lkal.Record{}, false
}

func listField(rec lkal.Record, key string) (lkal.List, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			l, ok := f.Value.(lkal.List)
			return l, ok
		}
	}
	return nil, false
}

func stringField(rec lkal.Record, key string) (string, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			s, ok := f.Value.(lkal.String)
			return string(s), ok
		}
	}
	return "", false
}

func intField(rec lkal.Record, key string) (int64, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			n, ok := f.Value.(lkal.Int)
			return int64(n), ok
		}
	}
	return 0, false
}

func optionalEnumField(rec lkal.Record, key string) string {
	for _, f := range rec.Fields {
		if f.Key == key {
			if e, ok := f.Value.(lkal.Enum); ok {
				return string(e)
			}
		}
	}
	return ""
}
