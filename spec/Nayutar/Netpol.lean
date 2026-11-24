set_option autoImplicit false

namespace Nayutar.Netpol

/-- IP address family (`ipv4` = 32-bit, `ipv6` = 128-bit). -/
inductive Family where
  | ipv4
  | ipv6
  deriving Repr, DecidableEq

/-- Bit width of an address in `fam`. -/
def Family.maxBits : Family → Nat
  | .ipv4 => 32
  | .ipv6 => 128

/-- Canonical sort rank (`ipv4 < ipv6`). -/
def Family.rank : Family → Nat
  | .ipv4 => 0
  | .ipv6 => 1

/-- Canonical CIDR prefix represented as a dyadic interval `[addr, addr + 2^(maxBits - bits))`. -/
structure Prefix where
  family : Family
  addr : Nat
  bits : Nat
  deriving Repr, DecidableEq

/-- Construct a `Prefix` from 64-bit high/low words (`addr = hi * 2^64 + lo`). -/
def Prefix.fromHiLo (fam : Family) (hi lo bits : Nat) : Prefix :=
  { family := fam, addr := hi * (2 ^ 64) + lo, bits := bits }

/-- Number of addresses covered by `p` (`2^(maxBits - bits)`). -/
def Prefix.size (p : Prefix) : Nat :=
  2 ^ (p.family.maxBits - p.bits)

/-- Whether `p` has a valid prefix length, in-range address, and zero host bits. -/
def Prefix.valid (p : Prefix) : Bool :=
  let mb := p.family.maxBits
  decide (p.bits ≤ mb ∧ p.addr < 2 ^ mb ∧ p.addr % (2 ^ (mb - p.bits)) = 0)

/-- Whether `(fam, addr)` lies within the dyadic interval of `p`. -/
def Prefix.contains (p : Prefix) (fam : Family) (addr : Nat) : Bool :=
  p.family == fam && decide (p.addr ≤ addr ∧ addr < p.addr + p.size)

/-- Whether two prefixes share the same family and have intersecting intervals. -/
def Prefix.overlaps (a b : Prefix) : Bool :=
  a.family == b.family &&
    decide (a.addr < b.addr + b.size ∧ b.addr < a.addr + a.size)

/-- Intra-tenant default traffic policy. -/
inductive IntraTenantMode where
  | allow
  | deny
  deriving Repr, DecidableEq

/-- Rule traffic direction relative to the owning tenant. -/
inductive Direction where
  | ingress
  | egress
  deriving Repr, DecidableEq

/-- Rule peer selector kind (`tenant` or `cidr`). -/
inductive PeerKind where
  | tenant
  | cidr
  deriving Repr, DecidableEq

/-- L4 protocol selector on a policy rule. -/
inductive RuleProto where
  | any
  | tcp
  | udp
  | icmp
  deriving Repr, DecidableEq

/-- L4 protocol carried by a packet. -/
inductive PacketProto where
  | tcp
  | udp
  | icmp
  | other
  deriving Repr, DecidableEq

/-- Netfilter conntrack state of a packet. -/
inductive ConnState where
  | new
  | established
  | related
  | invalid
  deriving Repr, DecidableEq

/-- Packet forwarding verdict. -/
inductive Verdict where
  | accept
  | drop
  deriving Repr, DecidableEq

/-- One ordered allow rule belonging to a tenant. -/
structure Rule where
  order : Int
  direction : Direction
  peerKind : PeerKind
  peerTenant : String
  peerCIDR : Prefix
  protocol : RuleProto
  hasPorts : Bool
  portMin : Nat
  portMax : Nat
  deriving Repr, DecidableEq

/-- Policy declaration for one tenant. -/
structure TenantPolicy where
  tenant : String
  intraTenant : IntraTenantMode
  cidrs : List Prefix
  rules : List Rule
  deriving Repr, DecidableEq

/-- Input bundle of tenant policies before validation and canonical sorting. -/
structure PolicyBundle where
  tenants : List TenantPolicy
  deriving Repr, DecidableEq

/-- Validation failure codes returned by `compile`. -/
inductive CompileError where
  | invalidTenantName
  | duplicateTenant
  | emptyTenantCIDRs
  | invalidCIDR
  | duplicateTenantCIDR
  | overlappingTenantCIDRs
  | duplicateRuleOrder
  | unknownPeerTenant
  | invalidPortRange
  deriving Repr, DecidableEq

/-- Validated and canonically sorted policy bundle. -/
structure CompiledPolicy where
  tenants : List TenantPolicy
  deriving Repr, DecidableEq

/-- Packet evaluated by `evaluate` and `evalNft`. -/
structure Packet where
  family : Family
  srcAddr : Nat
  dstAddr : Nat
  protocol : PacketProto
  dstPort : Nat
  connState : ConnState
  deriving Repr, DecidableEq

/-- Lowercase ASCII digit or letter `[a-z0-9]`. -/
def isLowerAlnum (c : Char) : Bool :=
  let n := c.toNat
  decide ((48 ≤ n ∧ n ≤ 57) ∨ (97 ≤ n ∧ n ≤ 122))

/-- Lowercase RFC 1123 label interior character `[a-z0-9-]`. -/
def isLabelChar (c : Char) : Bool :=
  isLowerAlnum c || c == '-'

/-- Validate the tail of a lowercase RFC 1123 label (must end in `[a-z0-9]`). -/
def allLabelTail : List Char → Bool
  | [] => false
  | [c] => isLowerAlnum c
  | c :: cs => isLabelChar c && allLabelTail cs

/-- Validate a non-empty list of characters as a lowercase RFC 1123 label. -/
def isLabelChars : List Char → Bool
  | [] => false
  | [c] => isLowerAlnum c
  | c :: cs => isLowerAlnum c && allLabelTail cs

/-- Whether `s` is a valid lowercase RFC 1123 tenant label (`1..63` chars of `[a-z0-9-]`, starting and ending in `[a-z0-9]`). -/
def isValidTenantName (s : String) : Bool :=
  let cs := s.toList
  decide (1 ≤ cs.length ∧ cs.length ≤ 63) && isLabelChars cs

/-- Strict lexicographical comparison on character lists by code point (ASCII byte order on RFC 1123 labels). -/
def listCharLt : List Char → List Char → Bool
  | [], [] => false
  | [], _ :: _ => true
  | _ :: _, [] => false
  | a :: as, b :: bs =>
    if a.toNat < b.toNat then true
    else if b.toNat < a.toNat then false
    else listCharLt as bs

/-- Strict lexicographical comparison on strings. -/
def strLt (a b : String) : Bool :=
  listCharLt a.toList b.toList

/-- Insert `t` into a tenant list sorted ascending by `tenant`. -/
def insertTenant (t : TenantPolicy) : List TenantPolicy → List TenantPolicy
  | [] => [t]
  | x :: xs =>
    if strLt t.tenant x.tenant then t :: x :: xs
    else x :: insertTenant t xs

/-- Canonically sort tenants ascending by `tenant`. -/
def sortTenants : List TenantPolicy → List TenantPolicy
  | [] => []
  | t :: ts => insertTenant t (sortTenants ts)

/-- Total order predicate on prefixes: `(family: ipv4 < ipv6, addr ascending, bits ascending)`. -/
def prefixLe (a b : Prefix) : Bool :=
  if a.family.rank != b.family.rank then
    decide (a.family.rank < b.family.rank)
  else if a.addr != b.addr then
    decide (a.addr < b.addr)
  else
    decide (a.bits ≤ b.bits)

/-- Insert `p` into a prefix list sorted by `prefixLe`. -/
def insertPrefix (p : Prefix) : List Prefix → List Prefix
  | [] => [p]
  | x :: xs =>
    if prefixLe p x then p :: x :: xs
    else x :: insertPrefix p xs

/-- Canonically sort prefixes by `(family, addr, bits)`. -/
def sortPrefixes : List Prefix → List Prefix
  | [] => []
  | p :: ps => insertPrefix p (sortPrefixes ps)

/-- Insert `r` into a rule list sorted ascending by `order`. -/
def insertRule (r : Rule) : List Rule → List Rule
  | [] => [r]
  | x :: xs =>
    if decide (r.order ≤ x.order) then r :: x :: xs
    else x :: insertRule r xs

/-- Canonically sort rules ascending by `order`. -/
def sortRules : List Rule → List Rule
  | [] => []
  | r :: rs => insertRule r (sortRules rs)

/-- Validate one tenant's CIDR list in input order for validity and intra-tenant non-overlap. -/
def validateTenantCIDRs (seen : List Prefix) : List Prefix → Except CompileError Unit
  | [] => .ok ()
  | c :: rest =>
    if !c.valid then
      .error .invalidCIDR
    else if seen.any (Prefix.overlaps c) then
      .error .duplicateTenantCIDR
    else
      validateTenantCIDRs (c :: seen) rest

/-- Pass 1: validate tenant names, duplicates, non-empty CIDRs, and per-tenant CIDR validity/disjointness. -/
def pass1ValidateTenants (seenNames : List String) : List TenantPolicy → Except CompileError Unit
  | [] => .ok ()
  | t :: rest =>
    if !isValidTenantName t.tenant then
      .error .invalidTenantName
    else if seenNames.contains t.tenant then
      .error .duplicateTenant
    else if t.cidrs.isEmpty then
      .error .emptyTenantCIDRs
    else do
      validateTenantCIDRs [] t.cidrs
      pass1ValidateTenants (t.tenant :: seenNames) rest

/-- Whether any CIDR in `cs1` overlaps any CIDR in `cs2`. -/
def cidrsOverlap (cs1 cs2 : List Prefix) : Bool :=
  cs1.any (fun c1 => cs2.any (fun c2 => Prefix.overlaps c1 c2))

/-- Pass 3: check pairwise CIDR disjointness across distinct sorted tenants. -/
def checkCrossTenantDisjoint : List TenantPolicy → Except CompileError Unit
  | [] => .ok ()
  | t :: rest =>
    if rest.any (fun u => cidrsOverlap t.cidrs u.cidrs) then
      .error .overlappingTenantCIDRs
    else
      checkCrossTenantDisjoint rest

/-- Whether `rs` contains duplicate `order` values. -/
def hasDupRuleOrder : List Rule → Bool
  | [] => false
  | r :: rest => rest.any (fun r' => r'.order == r.order) || hasDupRuleOrder rest

/-- Whether a tenant named `name` exists in `tenants`. -/
def tenantNameExists (tenants : List TenantPolicy) (name : String) : Bool :=
  tenants.any (fun t => t.tenant == name)

/-- Validate one sorted rule's peer selector and optional port range. -/
def validateRule (allTenants : List TenantPolicy) (r : Rule) : Except CompileError Unit :=
  match r.peerKind with
  | .tenant =>
    if !tenantNameExists allTenants r.peerTenant then
      .error .unknownPeerTenant
    else if r.hasPorts && !((r.protocol == .tcp || r.protocol == .udp) &&
        decide (1 ≤ r.portMin ∧ r.portMin ≤ r.portMax ∧ r.portMax ≤ 65535)) then
      .error .invalidPortRange
    else
      .ok ()
  | .cidr =>
    if !r.peerCIDR.valid then
      .error .invalidCIDR
    else if r.hasPorts && !((r.protocol == .tcp || r.protocol == .udp) &&
        decide (1 ≤ r.portMin ∧ r.portMin ≤ r.portMax ∧ r.portMax ≤ 65535)) then
      .error .invalidPortRange
    else
      .ok ()

/-- Validate a list of sorted rules in order. -/
def validateRules (allTenants : List TenantPolicy) : List Rule → Except CompileError Unit
  | [] => .ok ()
  | r :: rest => do
    validateRule allTenants r
    validateRules allTenants rest

/-- Pass 4: validate and canonically sort rules for every sorted tenant. -/
def pass4ValidateAndSortRules (allTenants : List TenantPolicy) :
    List TenantPolicy → Except CompileError (List TenantPolicy)
  | [] => .ok []
  | t :: rest =>
    if hasDupRuleOrder t.rules then
      .error .duplicateRuleOrder
    else do
      let sortedRules := sortRules t.rules
      validateRules allTenants sortedRules
      let tail ← pass4ValidateAndSortRules allTenants rest
      .ok ({ t with rules := sortedRules } :: tail)

/-- Validate and canonically compile a `PolicyBundle` in 4 deterministic passes. -/
def compile (bundle : PolicyBundle) : Except CompileError CompiledPolicy := do
  pass1ValidateTenants [] bundle.tenants
  let sortedTenants := sortTenants bundle.tenants
  checkCrossTenantDisjoint sortedTenants
  let withSortedCIDRs := sortedTenants.map (fun t => { t with cidrs := sortPrefixes t.cidrs })
  let finalTenants ← pass4ValidateAndSortRules withSortedCIDRs withSortedCIDRs
  .ok { tenants := finalTenants }

/-- Whether any CIDR of `t` contains `(fam, addr)`. -/
def tenantContains (t : TenantPolicy) (fam : Family) (addr : Nat) : Bool :=
  t.cidrs.any (fun c => c.contains fam addr)

/-- Find the tenant in `cp` whose CIDRs contain `(fam, addr)`, or `none` if external. -/
def findTenant (cp : CompiledPolicy) (fam : Family) (addr : Nat) : Option TenantPolicy :=
  cp.tenants.find? (fun t => tenantContains t fam addr)

/-- Find the tenant in `cp` with the given name. -/
def findTenantByName (cp : CompiledPolicy) (name : String) : Option TenantPolicy :=
  cp.tenants.find? (fun t => t.tenant == name)

/-- Whether rule protocol selector `rp` matches packet protocol `pp`. -/
def protoMatches (rp : RuleProto) (pp : PacketProto) : Bool :=
  match rp, pp with
  | .any, _ => true
  | .tcp, .tcp => true
  | .udp, .udp => true
  | .icmp, .icmp => true
  | _, _ => false

/-- Whether `r`'s peer selector matches `(fam, peerAddr)`. -/
def matchesPeer (cp : CompiledPolicy) (r : Rule) (fam : Family) (peerAddr : Nat) : Bool :=
  match r.peerKind with
  | .cidr => r.peerCIDR.contains fam peerAddr
  | .tenant =>
    match findTenantByName cp r.peerTenant with
    | some peer => tenantContains peer fam peerAddr
    | none => false

/-- Whether rule `r` matches `pkt` in direction `dir`. -/
def matchesRule (cp : CompiledPolicy) (r : Rule) (dir : Direction) (pkt : Packet) : Bool :=
  let peerAddr := match dir with
    | .egress => pkt.dstAddr
    | .ingress => pkt.srcAddr
  r.direction == dir &&
    matchesPeer cp r pkt.family peerAddr &&
    protoMatches r.protocol pkt.protocol &&
    (!r.hasPorts || decide (r.portMin ≤ pkt.dstPort ∧ pkt.dstPort ≤ r.portMax))

/-- Whether any rule of tenant `t` matches `pkt` in direction `dir`. -/
def matchesTenantRules (cp : CompiledPolicy) (t : TenantPolicy) (dir : Direction) (pkt : Packet) : Bool :=
  t.rules.any (fun r => matchesRule cp r dir pkt)

/-- Declarative network policy evaluation of `pkt` under `cp`. -/
def evaluate (cp : CompiledPolicy) (pkt : Packet) : Verdict :=
  match pkt.connState with
  | .invalid => .drop
  | .established | .related => .accept
  | .new =>
    match findTenant cp pkt.family pkt.srcAddr, findTenant cp pkt.family pkt.dstAddr with
    | some srcT, some dstT =>
      if srcT.tenant == dstT.tenant && srcT.intraTenant == .allow then
        .accept
      else if matchesTenantRules cp srcT .egress pkt && matchesTenantRules cp dstT .ingress pkt then
        .accept
      else
        .drop
    | some srcT, none =>
      if matchesTenantRules cp srcT .egress pkt then .accept else .drop
    | none, some dstT =>
      if matchesTenantRules cp dstT .ingress pkt then .accept else .drop
    | none, none =>
      .drop

/-- Base chain name in `table inet nayutar`. -/
def forwardChainName : String := "forward"

/-- Stage 1 egress chain name for `tenant`. -/
def tenantChainName (tenant : String) : String :=
  "tenant_" ++ tenant

/-- Stage 2 ingress chain name for `tenant`. -/
def tenantInChainName (tenant : String) : String :=
  "tenant_" ++ tenant ++ "_in"

/-- Action executed when an `nftables` rule matches. -/
inductive NftAction where
  | accept
  | drop
  | ret
  | jump (target : String)
  deriving Repr, DecidableEq

/-- One `nftables` rule in the abstract table model. -/
structure NftRule where
  connStates : List ConnState := []
  srcPrefix : Option Prefix := none
  dstPrefix : Option Prefix := none
  protocol : Option RuleProto := none
  hasPorts : Bool := false
  portMin : Nat := 0
  portMax : Nat := 0
  action : NftAction
  deriving Repr, DecidableEq

/-- One named `nftables` chain. -/
structure NftChain where
  name : String
  rules : List NftRule
  deriving Repr, DecidableEq

/-- Abstract `table inet nayutar` with base chain `forward` (`policy drop`) and regular tenant chains. -/
structure NftTable where
  baseChain : NftChain
  basePolicy : Verdict := .drop
  chains : List NftChain
  deriving Repr, DecidableEq

/-- Total number of chains in `tbl` (base chain plus regular chains). -/
def NftTable.chainCount (tbl : NftTable) : Nat :=
  1 + tbl.chains.length

/-- Number of rules in the base `forward` chain of `tbl`. -/
def NftTable.forwardRuleCount (tbl : NftTable) : Nat :=
  tbl.baseChain.rules.length

/-- Expand the peer prefixes referenced by rule `r`. -/
def rulePeerPrefixes (cp : CompiledPolicy) (r : Rule) : List Prefix :=
  match r.peerKind with
  | .cidr => [r.peerCIDR]
  | .tenant =>
    match findTenantByName cp r.peerTenant with
    | some peer => peer.cidrs
    | none => []

/-- Compile one egress rule into `ret` rules for `tenant_<T>`. -/
def compileEgressRule (cp : CompiledPolicy) (r : Rule) : List NftRule :=
  if r.direction == .egress then
    (rulePeerPrefixes cp r).map (fun p => {
      dstPrefix := some p
      protocol := some r.protocol
      hasPorts := r.hasPorts
      portMin := r.portMin
      portMax := r.portMax
      action := .ret
    })
  else
    []

/-- Compile one ingress rule into `accept` rules for `tenant_<U>_in`. -/
def compileIngressRule (cp : CompiledPolicy) (r : Rule) : List NftRule :=
  if r.direction == .ingress then
    (rulePeerPrefixes cp r).map (fun p => {
      srcPrefix := some p
      protocol := some r.protocol
      hasPorts := r.hasPorts
      portMin := r.portMin
      portMax := r.portMax
      action := .accept
    })
  else
    []

/-- Compile the Stage 1 egress chain `tenant_<T>` for tenant `t`. -/
def compileEgressChain (cp : CompiledPolicy) (t : TenantPolicy) : NftChain :=
  let intraRules : List NftRule :=
    if t.intraTenant == .allow then
      t.cidrs.map (fun c => { dstPrefix := some c, action := .accept })
    else
      []
  let egressRules : List NftRule := t.rules.flatMap (compileEgressRule cp)
  { name := tenantChainName t.tenant
    rules := intraRules ++ egressRules ++ [{ action := .drop }] }

/-- Compile the Stage 2 ingress chain `tenant_<U>_in` for tenant `u`. -/
def compileIngressChain (cp : CompiledPolicy) (u : TenantPolicy) : NftChain :=
  let ingressRules : List NftRule := u.rules.flatMap (compileIngressRule cp)
  { name := tenantInChainName u.tenant
    rules := ingressRules ++ [{ action := .drop }] }

/-- Stage 1 forward-chain jump rules for one tenant. -/
def stage1RulesForTenant (t : TenantPolicy) : List NftRule :=
  t.cidrs.map (fun c => { srcPrefix := some c, action := .jump (tenantChainName t.tenant) })

/-- Stage 2 forward-chain jump rules for one tenant. -/
def stage2RulesForTenant (t : TenantPolicy) : List NftRule :=
  t.cidrs.map (fun c => { dstPrefix := some c, action := .jump (tenantInChainName t.tenant) })

/-- Stage 3 forward-chain external-egress completion rules for one tenant. -/
def stage3RulesForTenant (t : TenantPolicy) : List NftRule :=
  t.cidrs.map (fun c => { srcPrefix := some c, action := .accept })

/-- Compile a `CompiledPolicy` into the `nftables` table AST (`table inet nayutar`). -/
def compileNft (cp : CompiledPolicy) : NftTable :=
  let ctRules : List NftRule := [
    { connStates := [.invalid], action := .drop },
    { connStates := [.established, .related], action := .accept }
  ]
  let stage1 := cp.tenants.flatMap stage1RulesForTenant
  let stage2 := cp.tenants.flatMap stage2RulesForTenant
  let stage3 := cp.tenants.flatMap stage3RulesForTenant
  let chains := cp.tenants.flatMap (fun t => [compileEgressChain cp t, compileIngressChain cp t])
  { baseChain := { name := forwardChainName, rules := ctRules ++ stage1 ++ stage2 ++ stage3 }
    basePolicy := .drop
    chains := chains }

/-- Whether `r` matches `pkt`. -/
def nftRuleMatches (r : NftRule) (pkt : Packet) : Bool :=
  (r.connStates.isEmpty || r.connStates.contains pkt.connState) &&
  (match r.srcPrefix with | some p => p.contains pkt.family pkt.srcAddr | none => true) &&
  (match r.dstPrefix with | some p => p.contains pkt.family pkt.dstAddr | none => true) &&
  (match r.protocol with | some rp => protoMatches rp pkt.protocol | none => true) &&
  (!r.hasPorts || decide (r.portMin ≤ pkt.dstPort ∧ pkt.dstPort ≤ r.portMax))

/-- Look up a regular chain by name. -/
def findChain (chains : List NftChain) (name : String) : Option NftChain :=
  chains.find? (fun c => c.name == name)

/-- Execute a list of `nftables` rules with bounded jump depth `fuel`. -/
def evalRules (chains : List NftChain) (fuel : Nat) (rules : List NftRule) (pkt : Packet) :
    Option Verdict :=
  match rules with
  | [] => none
  | r :: rs =>
    if nftRuleMatches r pkt then
      match r.action with
      | .accept => some .accept
      | .drop => some .drop
      | .ret => none
      | .jump target =>
        match fuel with
        | 0 => some .drop
        | fuel' + 1 =>
          match findChain chains target with
          | none => some .drop
          | some sub =>
            match evalRules chains fuel' sub.rules pkt with
            | some v => some v
            | none => evalRules chains (fuel' + 1) rs pkt
    else
      evalRules chains fuel rs pkt
termination_by (fuel, rules.length)

/-- Evaluate `pkt` against a compiled `NftTable`. -/
def evalNft (tbl : NftTable) (pkt : Packet) : Verdict :=
  match evalRules tbl.chains (tbl.chains.length + 1) tbl.baseChain.rules pkt with
  | some v => v
  | none => tbl.basePolicy

end Nayutar.Netpol
