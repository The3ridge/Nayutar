package node

import (
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

//go:embed node.schema.lkal
var nodeSchemaBytes []byte

var compiledNodeSchema *schema.Schema

func init() {
	s, err := schema.Compile("node.schema.lkal", nodeSchemaBytes)
	if err != nil {
		panic("node: embedded node.schema.lkal failed to compile: " + err.Error())
	}
	compiledNodeSchema = s
}

// Schema returns the compiled embedded LKAL v0.2 schema for lk-node configurations.
func Schema() *schema.Schema {
	return compiledNodeSchema
}

// ContainerdConfig holds containerd client connection settings.
type ContainerdConfig struct {
	Socket      string
	Namespace   string
	Snapshotter string
}

// CNIRangeConfig defines one host-local IPAM address range entry.
type CNIRangeConfig struct {
	Subnet     string
	Gateway    string
	RangeStart string
	RangeEnd   string
}

// CNIConfig holds CNI plugin and Linux netns settings.
type CNIConfig struct {
	BinDir   string
	NetnsDir string
	Network  string
	Bridge   string
	DataDir  string
	Ranges   []CNIRangeConfig
}

// NetpolConfig configures optional host nftables network policy enforcement before unit startup.
type NetpolConfig struct {
	PolicyFile string
	NftBin     string
}

// UnitSpec describes one managed container unit and its lifecycle parameters.
type UnitSpec struct {
	ID         string
	Tenant     string
	Image      string
	Command    []string
	Args       []string
	Env        []string
	Mounts     []Mount
	WorkingDir string
	Lifecycle  lifecycle.Spec
}

// Config is the validated, default-completed configuration for lk-node.
type Config struct {
	Containerd ContainerdConfig
	CNI        CNIConfig
	Unit       UnitSpec
	Netpol     *NetpolConfig
}

// DecodeConfig parses, completes defaults, validates against Schema(), and decodes an LKAL config.
func DecodeConfig(name string, src []byte) (Config, error) {
	f, err := lkal.Parse(name, src)
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", name, err)
	}
	rawBridgeExplicit := false
	if rawCNI, ok := recordField(f.Data(), "cni"); ok {
		rawBridgeExplicit = hasField(rawCNI, "bridge")
	}
	completed, err := compiledNodeSchema.Complete(f.Data())
	if err != nil {
		return Config{}, fmt.Errorf("complete %s: %w", name, err)
	}
	if err := compiledNodeSchema.ValidateValue(completed); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", name, err)
	}
	root := completed.(lkal.Record)

	ctrRec, _ := recordField(root, "containerd")
	cniRec, _ := recordField(root, "cni")
	unitRec, _ := recordField(root, "unit")

	cfg := Config{
		Containerd: ContainerdConfig{
			Socket:      mustStringField(ctrRec, "socket"),
			Namespace:   mustStringField(ctrRec, "namespace"),
			Snapshotter: mustStringField(ctrRec, "snapshotter"),
		},
		CNI: CNIConfig{
			BinDir:   mustStringField(cniRec, "binDir"),
			NetnsDir: mustStringField(cniRec, "netnsDir"),
			Network:  mustStringField(cniRec, "network"),
			Bridge:   mustStringField(cniRec, "bridge"),
			DataDir:  optionalStringField(cniRec, "dataDir"),
		},
	}

	if !filepath.IsAbs(cfg.Containerd.Socket) {
		return Config{}, fmt.Errorf("validate %s: containerd.socket %q must be an absolute path", name, cfg.Containerd.Socket)
	}
	if !filepath.IsAbs(cfg.CNI.BinDir) {
		return Config{}, fmt.Errorf("validate %s: cni.binDir %q must be an absolute path", name, cfg.CNI.BinDir)
	}
	if !filepath.IsAbs(cfg.CNI.NetnsDir) || filepath.Clean(cfg.CNI.NetnsDir) == "/" {
		return Config{}, fmt.Errorf("validate %s: cni.netnsDir %q must be an absolute non-root path", name, cfg.CNI.NetnsDir)
	}
	if cfg.CNI.DataDir != "" && !filepath.IsAbs(cfg.CNI.DataDir) {
		return Config{}, fmt.Errorf("validate %s: cni.dataDir %q must be an absolute path", name, cfg.CNI.DataDir)
	}

	rangesRec, _ := recordField(cniRec, "ranges")
	type namedRange struct {
		name string
		rc   CNIRangeConfig
	}
	namedRanges := make([]namedRange, 0, len(rangesRec.Fields))
	for _, rf := range rangesRec.Fields {
		rRec := rf.Value.(lkal.Record)
		rc := CNIRangeConfig{
			Subnet:     mustStringField(rRec, "subnet"),
			Gateway:    optionalStringField(rRec, "gateway"),
			RangeStart: optionalStringField(rRec, "rangeStart"),
			RangeEnd:   optionalStringField(rRec, "rangeEnd"),
		}
		if err := validateCNIRange(rc); err != nil {
			return Config{}, fmt.Errorf("validate %s: cni.ranges.%s: %w", name, rf.Key, err)
		}
		namedRanges = append(namedRanges, namedRange{name: rf.Key, rc: rc})
	}
	slices.SortFunc(namedRanges, func(a, b namedRange) int {
		return strings.Compare(a.name, b.name)
	})
	cfg.CNI.Ranges = make([]CNIRangeConfig, len(namedRanges))
	for i, nr := range namedRanges {
		cfg.CNI.Ranges[i] = nr.rc
	}

	restartName := mustEnumField(unitRec, "restart")
	policy, err := parseRestartPolicy(restartName)
	if err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", name, err)
	}

	unitID := mustStringField(unitRec, "id")
	if strings.Contains(unitID, "..") {
		return Config{}, fmt.Errorf("validate %s: unit.id %q must not contain \"..\"", name, unitID)
	}
	unitTenant := optionalStringField(unitRec, "tenant")
	if unitTenant != "" {
		if err := keyspace.ValidateTenant(unitTenant); err != nil {
			return Config{}, fmt.Errorf("validate %s: unit.tenant %q: %w", name, unitTenant, err)
		}
		if !hasField(root, "netpol") {
			return Config{}, fmt.Errorf("validate %s: unit.tenant %q requires a netpol block", name, unitTenant)
		}
		tenantBridge := netpol.TenantBridgeName(unitTenant)
		if rawBridgeExplicit && cfg.CNI.Bridge != tenantBridge {
			return Config{}, fmt.Errorf("validate %s: cni.bridge %q must equal the tenant bridge %q for unit.tenant %q", name, cfg.CNI.Bridge, tenantBridge, unitTenant)
		}
		cfg.CNI.Bridge = tenantBridge
	} else if netpol.IsTenantBridgeName(cfg.CNI.Bridge) {
		return Config{}, fmt.Errorf("validate %s: cni.bridge %q uses the reserved %q prefix but unit.tenant is not set", name, cfg.CNI.Bridge, netpol.TenantBridgePrefix)
	}

	if npRec, ok := recordField(root, "netpol"); ok {
		policyFile := mustStringField(npRec, "policyFile")
		nftBin := mustStringField(npRec, "nftBin")
		if !filepath.IsAbs(policyFile) || filepath.Clean(policyFile) != policyFile {
			return Config{}, fmt.Errorf("validate %s: netpol.policyFile %q must be a clean absolute path", name, policyFile)
		}
		if !filepath.IsAbs(nftBin) || filepath.Clean(nftBin) != nftBin {
			return Config{}, fmt.Errorf("validate %s: netpol.nftBin %q must be a clean absolute path", name, nftBin)
		}
		cfg.Netpol = &NetpolConfig{
			PolicyFile: policyFile,
			NftBin:     nftBin,
		}
	}

	var mounts []Mount
	if mountRec, ok := recordField(unitRec, "mounts"); ok && len(mountRec.Fields) > 0 {
		type namedMount struct {
			name string
			m    Mount
		}
		namedMounts := make([]namedMount, 0, len(mountRec.Fields))
		for _, mf := range mountRec.Fields {
			mRec := mf.Value.(lkal.Record)
			m := Mount{
				Source:      mustStringField(mRec, "source"),
				Destination: mustStringField(mRec, "destination"),
				Type:        mustStringField(mRec, "type"),
				Options:     optionalStringList(mRec, "options"),
			}
			if !strings.HasPrefix(m.Destination, "/") || filepath.Clean(m.Destination) != m.Destination {
				return Config{}, fmt.Errorf("validate %s: unit.mounts.%s.destination %q must be a clean absolute path", name, mf.Key, m.Destination)
			}
			if filepath.Clean(m.Destination) == "/" {
				return Config{}, fmt.Errorf("validate %s: unit.mounts.%s.destination must not be root \"/\"", name, mf.Key)
			}
			if m.Type == "bind" && (!filepath.IsAbs(m.Source) || filepath.Clean(m.Source) != m.Source) {
				return Config{}, fmt.Errorf("validate %s: unit.mounts.%s.source %q must be a clean absolute path for bind mount", name, mf.Key, m.Source)
			}
			namedMounts = append(namedMounts, namedMount{name: mf.Key, m: m})
		}
		slices.SortFunc(namedMounts, func(a, b namedMount) int {
			return strings.Compare(a.name, b.name)
		})
		mounts = make([]Mount, len(namedMounts))
		for i, nm := range namedMounts {
			mounts[i] = nm.m
		}
	}

	workingDir := optionalStringField(unitRec, "workingDir")
	if workingDir != "" && (!strings.HasPrefix(workingDir, "/") || filepath.Clean(workingDir) != workingDir) {
		return Config{}, fmt.Errorf("validate %s: unit.workingDir %q must be a clean absolute path", name, workingDir)
	}

	var env []string
	if envRec, ok := recordField(unitRec, "env"); ok && len(envRec.Fields) > 0 {
		env = make([]string, 0, len(envRec.Fields))
		for _, ef := range envRec.Fields {
			env = append(env, ef.Key+"="+string(ef.Value.(lkal.String)))
		}
		slices.Sort(env)
	}

	lc := lifecycle.Spec{
		RestartPolicy:  policy,
		StopGraceMs:    mustIntField(unitRec, "stopGraceMs"),
		BackoffBaseMs:  mustIntField(unitRec, "backoffBaseMs"),
		BackoffMaxMs:   mustIntField(unitRec, "backoffMaxMs"),
		BackoffResetMs: mustIntField(unitRec, "backoffResetMs"),
		CNICount:       2, // loopback followed by bridge + host-local
	}
	if err := lc.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", name, err)
	}

	cfg.Unit = UnitSpec{
		ID:         unitID,
		Tenant:     unitTenant,
		Image:      mustStringField(unitRec, "image"),
		Command:    optionalStringList(unitRec, "command"),
		Args:       optionalStringList(unitRec, "args"),
		Env:        env,
		Mounts:     mounts,
		WorkingDir: workingDir,
		Lifecycle:  lc,
	}
	return cfg, nil
}

func validateCNIRange(rc CNIRangeConfig) error {
	pfx, err := netip.ParsePrefix(rc.Subnet)
	if err != nil {
		return fmt.Errorf("invalid subnet %q: %w", rc.Subnet, err)
	}
	checkAddr := func(label, raw string) error {
		if raw == "" {
			return nil
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil || addr.Zone() != "" {
			return fmt.Errorf("invalid %s %q", label, raw)
		}
		if !pfx.Contains(addr) {
			return fmt.Errorf("%s %q is outside subnet %s", label, raw, rc.Subnet)
		}
		return nil
	}
	if err := checkAddr("gateway", rc.Gateway); err != nil {
		return err
	}
	if err := checkAddr("rangeStart", rc.RangeStart); err != nil {
		return err
	}
	if err := checkAddr("rangeEnd", rc.RangeEnd); err != nil {
		return err
	}
	if rc.RangeStart != "" && rc.RangeEnd != "" {
		s, _ := netip.ParseAddr(rc.RangeStart)
		e, _ := netip.ParseAddr(rc.RangeEnd)
		if e.Less(s) {
			return errors.New("rangeEnd must be >= rangeStart")
		}
	}
	return nil
}

func parseRestartPolicy(s string) (lifecycle.RestartPolicy, error) {
	switch s {
	case "never":
		return lifecycle.RestartNever, nil
	case "onFailure":
		return lifecycle.RestartOnFailure, nil
	case "always":
		return lifecycle.RestartAlways, nil
	default:
		return 0, fmt.Errorf("unknown restart policy %q", s)
	}
}

func hasField(rec lkal.Record, key string) bool {
	for _, f := range rec.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
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

func mustStringField(rec lkal.Record, key string) string {
	for _, f := range rec.Fields {
		if f.Key == key {
			return string(f.Value.(lkal.String))
		}
	}
	return ""
}

func optionalStringField(rec lkal.Record, key string) string {
	for _, f := range rec.Fields {
		if f.Key == key {
			if s, ok := f.Value.(lkal.String); ok {
				return string(s)
			}
		}
	}
	return ""
}

func mustEnumField(rec lkal.Record, key string) string {
	for _, f := range rec.Fields {
		if f.Key == key {
			return string(f.Value.(lkal.Enum))
		}
	}
	return ""
}

func mustIntField(rec lkal.Record, key string) int64 {
	for _, f := range rec.Fields {
		if f.Key == key {
			return int64(f.Value.(lkal.Int))
		}
	}
	return 0
}

func optionalStringList(rec lkal.Record, key string) []string {
	list, ok := listField(rec, key)
	if !ok || len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, el := range list {
		out[i] = string(el.(lkal.String))
	}
	return out
}
