import Lean.Data.Json
import Nayutar.Fixture.Register
import Nayutar.Drt.Store
import Nayutar.Drt.Keyspace
import Nayutar.Drt.WatchCache
import Nayutar.Drt.Liveness
import Nayutar.Drt.Envelope
import Nayutar.Drt.Refs
import Nayutar.Drt.OrderedSet

open Lean
open Nayutar.Fixture

namespace Nayutar.Drt

private structure Case where
  id : Nat
  ops : List Op

private def parseOp (j : Json) : Except String Op := do
  let opName ← (← j.getObjVal? "op").getStr?
  match opName with
  | "put" =>
    let v ← (← j.getObjVal? "value").getNat?
    return .put v
  | "delete" =>
    return .delete
  | "cas" =>
    let expect ← (← j.getObjVal? "expect").getNat?
    let v ← (← j.getObjVal? "value").getNat?
    return .cas expect v
  | other =>
    throw s!"unknown op: {other}"

private def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← (← j.getObjVal? "id").getNat?
  let opsArr ← (← j.getObjVal? "ops").getArr?
  let mut ops : List Op := []
  for elem in opsArr do
    let op ← parseOp elem
    ops := op :: ops
  return { id := id, ops := ops.reverse }

private def validateAll (parse : String → Except String α) (lines : Array (Nat × String)) :
    Except String (Array α) := do
  let mut cases : Array α := #[]
  for (lineNo, text) in lines do
    match parse text with
    | .ok c => cases := cases.push c
    | .error err => throw s!"line {lineNo}: {err}"
  return cases

private def stepToJson (out : Outcome) : Json :=
  let valJson : Json := match out.state.value with
    | none => Json.null
    | some v => toJson v
  Json.mkObj [
    ("ok", toJson out.ok),
    ("value", valJson),
    ("revision", toJson out.state.revision)
  ]

private def caseToJson (id : Nat) (outcomes : List Outcome) : Json :=
  Json.mkObj [
    ("id", toJson id),
    ("steps", Json.arr (outcomes.map stepToJson).toArray)
  ]

private partial def readLines (stdin : IO.FS.Stream) (lineNo : Nat)
    (acc : Array (Nat × String)) : IO (Array (Nat × String)) := do
  let line ← stdin.getLine
  if line.isEmpty then
    return acc
  else
    let acc' := if line.all Char.isWhitespace then acc else acc.push (lineNo, line)
    readLines stdin (lineNo + 1) acc'

end Nayutar.Drt

private def runSuite (parse : String → Except String α) (render : α → Lean.Json) : IO UInt32 := do
  let stdin ← IO.getStdin
  let lines ← Nayutar.Drt.readLines stdin 1 #[]
  match Nayutar.Drt.validateAll parse lines with
  | .error msg =>
    IO.eprintln msg
    return 1
  | .ok cases =>
    for c in cases do
      IO.println (render c).compress
    return 0

def main (args : List String) : IO UInt32 := do
  match args with
  | ["register"] =>
    runSuite Nayutar.Drt.parseCase
      (fun c => Nayutar.Drt.caseToJson c.id (Nayutar.Fixture.Register.run c.ops))
  | ["store"] =>
    runSuite Nayutar.Drt.Store.parseCase Nayutar.Drt.Store.caseToJson
  | ["keyspace"] =>
    runSuite Nayutar.Drt.Keyspace.parseCase Nayutar.Drt.Keyspace.caseToJson
  | ["watchcache"] =>
    runSuite Nayutar.Drt.WatchCache.parseCase Nayutar.Drt.WatchCache.caseToJson
  | ["liveness"] =>
    runSuite Nayutar.Drt.Liveness.parseCase Nayutar.Drt.Liveness.caseToJson
  | ["envelope"] =>
    runSuite Nayutar.Drt.Envelope.parseCase Nayutar.Drt.Envelope.caseToJson
  | ["refs"] =>
    runSuite Nayutar.Drt.Refs.parseCase Nayutar.Drt.Refs.caseToJson
  | ["orderedset"] =>
    runSuite Nayutar.Drt.OrderedSet.parseCase Nayutar.Drt.OrderedSet.caseToJson
  | _ =>
    IO.eprintln "usage: nayutar-drt register|store|keyspace|watchcache|liveness|envelope|refs|orderedset"
    return 2
