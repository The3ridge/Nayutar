import Lean.Data.Json
import Nayutar.Netpol

set_option autoImplicit false

open Lean
open Nayutar.Netpol

namespace Nayutar.Drt.Netpol

/-- Parsed test case for the `netpol` DRT suite. -/
structure Case where
  id : Nat
  bundle : PolicyBundle
  packets : List Packet

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def int (j : Json) (field : String) : Except String Int := do
  (← j.getObjVal? field).getInt?

private def bool (j : Json) (field : String) : Except String Bool := do
  (← j.getObjVal? field).getBool?

private def u64 (j : Json) (field : String) : Except String Nat := do
  let n ← nat j field
  if n < 2 ^ 64 then
    pure n
  else
    throw s!"{field} out of uint64 range: {n}"

private def list {α : Type} (j : Json) (field : String) (parse : Json → Except String α) :
    Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

private def parseFamily : String → Except String Family
  | "ipv4" => pure .ipv4
  | "ipv6" => pure .ipv6
  | other => throw s!"unknown family: {other}"

private def parseIntraTenant : String → Except String IntraTenantMode
  | "allow" => pure .allow
  | "" => pure .allow
  | "deny" => pure .deny
  | other => throw s!"unknown intra_tenant mode: {other}"

private def parseDirection : String → Except String Direction
  | "ingress" => pure .ingress
  | "egress" => pure .egress
  | other => throw s!"unknown direction: {other}"

private def parsePeerKind : String → Except String PeerKind
  | "tenant" => pure .tenant
  | "cidr" => pure .cidr
  | other => throw s!"unknown peer_kind: {other}"

private def parseRuleProto : String → Except String RuleProto
  | "any" => pure .any
  | "tcp" => pure .tcp
  | "udp" => pure .udp
  | "icmp" => pure .icmp
  | other => throw s!"unknown rule protocol: {other}"

private def parsePacketProto : String → Except String PacketProto
  | "tcp" => pure .tcp
  | "udp" => pure .udp
  | "icmp" => pure .icmp
  | "other" => pure .other
  | other => throw s!"unknown packet protocol: {other}"

private def parseConnState : String → Except String ConnState
  | "new" => pure .new
  | "established" => pure .established
  | "related" => pure .related
  | "invalid" => pure .invalid
  | other => throw s!"unknown conn_state: {other}"

private def parsePrefix (j : Json) : Except String Prefix := do
  let fam ← parseFamily (← str j "family")
  let hi ← u64 j "hi"
  let lo ← u64 j "lo"
  let bits ← nat j "bits"
  return Prefix.fromHiLo fam hi lo bits

private def parseRule (j : Json) : Except String Rule := do
  return {
    order := ← int j "order"
    direction := ← parseDirection (← str j "direction")
    peerKind := ← parsePeerKind (← str j "peer_kind")
    peerTenant := ← str j "peer_tenant"
    peerCIDR := ← parsePrefix (← j.getObjVal? "peer_cidr")
    protocol := ← parseRuleProto (← str j "protocol")
    hasPorts := ← bool j "has_ports"
    portMin := ← nat j "port_min"
    portMax := ← nat j "port_max"
  }

private def parseTenantPolicy (j : Json) : Except String TenantPolicy := do
  return {
    tenant := ← str j "tenant"
    intraTenant := ← parseIntraTenant (← str j "intra_tenant")
    cidrs := ← list j "cidrs" parsePrefix
    rules := ← list j "rules" parseRule
  }

private def parseBundle (j : Json) : Except String PolicyBundle := do
  return { tenants := ← list j "tenants" parseTenantPolicy }

private def parsePacket (j : Json) : Except String Packet := do
  let fam ← parseFamily (← str j "family")
  let srcHi ← u64 j "src_hi"
  let srcLo ← u64 j "src_lo"
  let dstHi ← u64 j "dst_hi"
  let dstLo ← u64 j "dst_lo"
  let proto ← parsePacketProto (← str j "protocol")
  let dstPort ← nat j "dst_port"
  let cs ← parseConnState (← str j "conn_state")
  return {
    family := fam
    srcAddr := srcHi * (2 ^ 64) + srcLo
    dstAddr := dstHi * (2 ^ 64) + dstLo
    protocol := proto
    dstPort := dstPort
    connState := cs
  }

/-- Parse one JSON line into a `netpol` DRT `Case`. -/
def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let bundle ← parseBundle (← j.getObjVal? "bundle")
  let packets ← list j "packets" parsePacket
  return { id := id, bundle := bundle, packets := packets }

private def compileErrorName : CompileError → String
  | .invalidTenantName => "invalid_tenant_name"
  | .duplicateTenant => "duplicate_tenant"
  | .emptyTenantCIDRs => "empty_tenant_cidrs"
  | .invalidCIDR => "invalid_cidr"
  | .duplicateTenantCIDR => "duplicate_tenant_cidr"
  | .overlappingTenantCIDRs => "overlapping_tenant_cidrs"
  | .duplicateRuleOrder => "duplicate_rule_order"
  | .unknownPeerTenant => "unknown_peer_tenant"
  | .invalidPortRange => "invalid_port_range"

private def verdictName : Verdict → String
  | .accept => "accept"
  | .drop => "drop"

private def familyName : Family → String
  | .ipv4 => "ipv4"
  | .ipv6 => "ipv6"

private def ruleProtoName : RuleProto → String
  | .any => "any"
  | .tcp => "tcp"
  | .udp => "udp"
  | .icmp => "icmp"

private def prefixText : Option Prefix → String
  | none => "-"
  | some p => s!"{familyName p.family}:{p.addr / 2 ^ 64}:{p.addr % 2 ^ 64}/{p.bits}"

private def connStateName : ConnState → String
  | .new => "new"
  | .established => "established"
  | .related => "related"
  | .invalid => "invalid"

private def connText : List ConnState → String
  | [] => "-"
  | [.invalid] => "invalid"
  | [.established, .related] => "established_related"
  | cs => "other:" ++ ",".intercalate (cs.map connStateName)

private def actionText : NftAction → String
  | .accept => "accept"
  | .drop => "drop"
  | .ret => "return"
  | .jump target => "jump:" ++ target

private def ruleText (r : NftRule) : String :=
  let proto := match r.protocol with
    | none => "-"
    | some rp => ruleProtoName rp
  let ports := if r.hasPorts then s!"{r.portMin}-{r.portMax}" else "-"
  s!"  rule ct={connText r.connStates} src={prefixText r.srcPrefix} dst={prefixText r.dstPrefix} proto={proto} ports={ports} action={actionText r.action}\n"

private def chainText (ch : NftChain) (base : Bool) (policy : String) : String :=
  s!"chain {ch.name} base={if base then 1 else 0} policy={policy}\n" ++ String.join (ch.rules.map ruleText)

/-- Canonical text of the whole compiled table, mirrored by the Go DRT test. -/
def tableText (tbl : NftTable) : String :=
  "table nayutar\n" ++
    chainText tbl.baseChain true (verdictName tbl.basePolicy) ++
    String.join (tbl.chains.map (fun ch => chainText ch false "-"))

/-- Evaluate `c` and render the DRT output JSON object. -/
def caseToJson (c : Case) : Json :=
  match compile c.bundle with
  | .error err =>
    Json.mkObj [
      ("id", toJson c.id),
      ("ok", toJson false),
      ("error", toJson (compileErrorName err)),
      ("chain_count", toJson (0 : Nat)),
      ("forward_rule_count", toJson (0 : Nat)),
      ("verdicts", Json.arr #[]),
      ("nft_matches_spec", toJson true),
      ("nft_table", toJson "")
    ]
  | .ok cp =>
    let tbl := compileNft cp
    let verdicts := c.packets.map (fun pkt => verdictName (evaluate cp pkt))
    let nftMatches := c.packets.all (fun pkt => evalNft tbl pkt == evaluate cp pkt)
    Json.mkObj [
      ("id", toJson c.id),
      ("ok", toJson true),
      ("error", toJson ""),
      ("chain_count", toJson tbl.chainCount),
      ("forward_rule_count", toJson tbl.forwardRuleCount),
      ("verdicts", Json.arr (verdicts.map toJson).toArray),
      ("nft_matches_spec", toJson nftMatches),
      ("nft_table", toJson (tableText tbl))
    ]

end Nayutar.Drt.Netpol
