import Lean.Data.Json
import Nayutar.Keyspace

open Lean
open Nayutar.Keyspace

namespace Nayutar.Drt.Keyspace

inductive Op where
  | encode (key : Key)
  | rel (key : Key)
  | parse (text : String)
  | parseRel (tenant : String) (text : String)

structure Case where
  id : Nat
  op : Op

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def parseFamily : String → Except String Family
  | "object" => pure .object
  | "ownership" => pure .ownership
  | "backref" => pure .backref
  | "binding" => pure .binding
  | "schema" => pure .schema
  | "tenant" => pure .tenant
  | "node" => pure .node
  | "eviction_budget" => pure .evictionBudget
  | other => throw s!"unknown family: {other}"

private def parseKey (j : Json) : Except String Key := do
  let family ← parseFamily (← str j "family")
  let tenant ← str j "tenant"
  let group ← str j "group"
  let kind ← str j "kind"
  let name ← str j "name"
  let sgroup ← str j "sgroup"
  let skind ← str j "skind"
  let sname ← str j "sname"
  let hash ← str j "hash"
  let version ← str j "version"
  let node ← str j "node"
  return {
    family := family
    tenant := tenant
    object := { group := group, kind := kind, name := name }
    source := { group := sgroup, kind := skind, name := sname }
    identityHash := hash
    version := version
    node := node
  }

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let op ← match ← str j "op" with
    | "encode" => pure (.encode (← parseKey (← j.getObjVal? "key")))
    | "rel" => pure (.rel (← parseKey (← j.getObjVal? "key")))
    | "parse" => pure (.parse (← str j "text"))
    | "parserel" => pure (.parseRel (← str j "tenant") (← str j "text"))
    | other => throw s!"unknown op: {other}"
  return { id := id, op := op }

private def familyName : Family → String
  | .object => "object"
  | .ownership => "ownership"
  | .backref => "backref"
  | .binding => "binding"
  | .schema => "schema"
  | .tenant => "tenant"
  | .node => "node"
  | .evictionBudget => "eviction_budget"

private def keyObjJson (family tenant group kind name sgroup skind sname hash version node : String) : Json :=
  Json.mkObj [
    ("family", toJson family),
    ("tenant", toJson tenant),
    ("group", toJson group),
    ("kind", toJson kind),
    ("name", toJson name),
    ("sgroup", toJson sgroup),
    ("skind", toJson skind),
    ("sname", toJson sname),
    ("hash", toJson hash),
    ("version", toJson version),
    ("node", toJson node)
  ]

private def emptyKeyJson : Json :=
  keyObjJson "" "" "" "" "" "" "" "" "" "" ""

private def keyToJson (k : Key) : Json :=
  keyObjJson
    (familyName k.family)
    k.tenant
    k.object.group
    k.object.kind
    k.object.name
    k.source.group
    k.source.kind
    k.source.name
    k.identityHash
    k.version
    k.node

def caseToJson (c : Case) : Json :=
  let (ok, text, keyJson) := match c.op with
    | .encode k =>
      match encode k with
      | some s => (true, s, emptyKeyJson)
      | none => (false, "", emptyKeyJson)
    | .rel k =>
      match rel k with
      | some s => (true, s, emptyKeyJson)
      | none => (false, "", emptyKeyJson)
    | .parse s =>
      match parse s with
      | some k => (true, "", keyToJson k)
      | none => (false, "", emptyKeyJson)
    | .parseRel tenant s =>
      match parseRel tenant s with
      | some k => (true, "", keyToJson k)
      | none => (false, "", emptyKeyJson)
  Json.mkObj [
    ("id", toJson c.id),
    ("ok", toJson ok),
    ("text", toJson text),
    ("key", keyJson)
  ]

end Nayutar.Drt.Keyspace
