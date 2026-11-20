namespace Nayutar.Keyspace

inductive Family where
  | object
  | ownership
  | backref
  | binding
  | schema
  | tenant
  | node
  | evictionBudget
  deriving Repr, DecidableEq

structure Object where
  group : String
  kind : String
  name : String
  deriving Repr, DecidableEq

structure Key where
  family : Family
  tenant : String
  object : Object
  source : Object
  identityHash : String
  version : String
  node : String
  deriving Repr, DecidableEq

private def emptyObject : Object :=
  { group := "", kind := "", name := "" }

private def isEmptyObject (o : Object) : Bool :=
  o.group == "" && o.kind == "" && o.name == ""

private def isAsciiPrintable (n : Nat) : Bool :=
  decide (32 ≤ n ∧ n < 128)

private def isLowerAlnum (c : Char) : Bool :=
  let n := c.toNat
  isAsciiPrintable n &&
    (decide (48 ≤ n ∧ n ≤ 57) || decide (97 ≤ n ∧ n ≤ 122))

private def isLabelChar (c : Char) : Bool :=
  isLowerAlnum c || c == '-'

private def isHexLower (c : Char) : Bool :=
  let n := c.toNat
  isAsciiPrintable n &&
    (decide (48 ≤ n ∧ n ≤ 57) || decide (97 ≤ n ∧ n ≤ 102))

private def allLabelTail : List Char → Bool
  | [] => false
  | [c] => isLowerAlnum c
  | c :: cs => isLabelChar c && allLabelTail cs

private def isLabelChars : List Char → Bool
  | [] => false
  | [c] => isLowerAlnum c
  | c :: cs => isLowerAlnum c && allLabelTail cs

private def isLabel (s : String) : Bool :=
  let cs := s.toList
  decide (1 ≤ cs.length ∧ cs.length ≤ 63) && isLabelChars cs

private def charsToString (cs : List Char) : String :=
  cs.foldl String.push ""

private def splitCharAux (sep : Char) (cur : String) (acc : List String) : List Char → List String
  | [] => (cur :: acc).reverse
  | c :: cs =>
    if c == sep then splitCharAux sep "" (cur :: acc) cs
    else splitCharAux sep (cur.push c) acc cs

private def splitChar (sep : Char) (s : String) : List String :=
  splitCharAux sep "" [] s.toList

private def splitFirstAux (sep : Char) (left : String) : List Char → Option (String × String)
  | [] => none
  | c :: cs =>
    if c == sep then some (left, charsToString cs)
    else splitFirstAux sep (left.push c) cs

private def splitFirstDot (s : String) : Option (String × String) :=
  splitFirstAux '.' "" s.toList

/-- The elements of a subdomain follow the label character rule but have no length bound of their own. -/
private def isSubdomain (s : String) : Bool :=
  let cs := s.toList
  decide (1 ≤ cs.length ∧ cs.length ≤ 253) && (splitChar '.' s).all (fun e => isLabelChars e.toList)

private def isMultiLabelSubdomain (s : String) : Bool :=
  isSubdomain s && decide ((splitChar '.' s).length ≥ 2)

private def isGroup (s : String) : Bool :=
  s == "core" || isMultiLabelSubdomain s

private def isIdentityHash (s : String) : Bool :=
  let cs := s.toList
  cs.length == 64 && cs.all isHexLower

private def isValidObject (o : Object) : Bool :=
  isGroup o.group && isLabel o.kind && isSubdomain o.name

private def encodeQualifiedKind (group kind : String) : String :=
  if group == "core" then kind else s!"{kind}.{group}"

private def parseQualifiedKind (s : String) : Option (String × String) :=
  match splitFirstDot s with
  | none =>
    if isLabel s then some ("core", s) else none
  | some (kind, group) =>
    if isLabel kind && isMultiLabelSubdomain group then some (group, kind) else none

private def relBody (k : Key) : Option String :=
  match k.family with
  | .object =>
    if isLabel k.tenant && isValidObject k.object && isEmptyObject k.source
        && k.identityHash == "" && k.version == "" && k.node == "" then
      some s!"o/{k.object.group}/{k.object.kind}/{k.object.name}"
    else none
  | .ownership =>
    if isLabel k.tenant && isValidObject k.object && isEmptyObject k.source
        && k.identityHash == "" && k.version == "" && k.node == "" then
      some s!"own/{k.object.group}/{k.object.kind}/{k.object.name}"
    else none
  | .backref =>
    if isLabel k.tenant && isValidObject k.object && isValidObject k.source
        && k.identityHash == "" && k.version == "" && k.node == "" then
      let tQKind := encodeQualifiedKind k.object.group k.object.kind
      let sQKind := encodeQualifiedKind k.source.group k.source.kind
      some s!"ref/{tQKind}/{k.object.name}/{sQKind}/{k.source.name}"
    else none
  | .binding =>
    if isLabel k.tenant && isIdentityHash k.identityHash && isEmptyObject k.object
        && isEmptyObject k.source && k.version == "" && k.node == "" then
      some s!"bind/{k.identityHash}"
    else none
  | .schema => none
  | .tenant => none
  | .node => none
  | .evictionBudget => none

/-- Encode `k` as a canonical tenant-relative key, or `none` if invalid or cluster-scoped. -/
def rel (k : Key) : Option String :=
  relBody k

/-- Encode `k` as a canonical absolute key, or `none` if any field is invalid or an unused field is non-empty. -/
def encode (k : Key) : Option String :=
  match k.family with
  | .object | .ownership | .backref | .binding =>
    match relBody k with
    | some body => some s!"/t/{k.tenant}/{body}"
    | none => none
  | .schema =>
    if isIdentityHash k.identityHash && isLabel k.version && k.tenant == ""
        && isEmptyObject k.object && isEmptyObject k.source && k.node == "" then
      some s!"/x/{k.identityHash}/schema/{k.version}"
    else none
  | .tenant =>
    if isLabel k.tenant && isEmptyObject k.object && isEmptyObject k.source
        && k.identityHash == "" && k.version == "" && k.node == "" then
      some s!"/sys/tenants/{k.tenant}"
    else none
  | .node =>
    if isSubdomain k.node && k.tenant == "" && isEmptyObject k.object
        && isEmptyObject k.source && k.identityHash == "" && k.version == "" then
      some s!"/sys/nodes/{k.node}"
    else none
  | .evictionBudget =>
    if k.tenant == "" && isEmptyObject k.object && isEmptyObject k.source
        && k.identityHash == "" && k.version == "" && k.node == "" then
      some "/sys/liveness/evictions"
    else none

private def parseRelSegments (tenant : String) : List String → Option Key
  | ["o", group, kind, name] =>
    if isGroup group && isLabel kind && isSubdomain name then
      some {
        family := .object
        tenant := tenant
        object := { group := group, kind := kind, name := name }
        source := emptyObject
        identityHash := ""
        version := ""
        node := ""
      }
    else none
  | ["own", group, kind, name] =>
    if isGroup group && isLabel kind && isSubdomain name then
      some {
        family := .ownership
        tenant := tenant
        object := { group := group, kind := kind, name := name }
        source := emptyObject
        identityHash := ""
        version := ""
        node := ""
      }
    else none
  | ["ref", tQKind, tName, sQKind, sName] =>
    match parseQualifiedKind tQKind, parseQualifiedKind sQKind with
    | some (tGroup, tKind), some (sGroup, sKind) =>
      if isSubdomain tName && isSubdomain sName then
        some {
          family := .backref
          tenant := tenant
          object := { group := tGroup, kind := tKind, name := tName }
          source := { group := sGroup, kind := sKind, name := sName }
          identityHash := ""
          version := ""
          node := ""
        }
      else none
    | _, _ => none
  | ["bind", hash] =>
    if isIdentityHash hash then
      some {
        family := .binding
        tenant := tenant
        object := emptyObject
        source := emptyObject
        identityHash := hash
        version := ""
        node := ""
      }
    else none
  | _ => none

/-- Parse a canonical tenant-relative key under `tenant`. -/
def parseRel (tenant relStr : String) : Option Key :=
  if isLabel tenant then
    match relStr.toList with
    | '/' :: _ => none
    | _ => parseRelSegments tenant (splitChar '/' relStr)
  else none

/-- Parse a canonical absolute key. -/
def parse (s : String) : Option Key :=
  match splitChar '/' s with
  | "" :: "t" :: tenant :: rest =>
    if isLabel tenant then parseRelSegments tenant rest else none
  | ["", "x", hash, "schema", version] =>
    if isIdentityHash hash && isLabel version then
      some {
        family := .schema
        tenant := ""
        object := emptyObject
        source := emptyObject
        identityHash := hash
        version := version
        node := ""
      }
    else none
  | ["", "sys", "tenants", tenant] =>
    if isLabel tenant then
      some {
        family := .tenant
        tenant := tenant
        object := emptyObject
        source := emptyObject
        identityHash := ""
        version := ""
        node := ""
      }
    else none
  | ["", "sys", "nodes", node] =>
    if isSubdomain node then
      some {
        family := .node
        tenant := ""
        object := emptyObject
        source := emptyObject
        identityHash := ""
        version := ""
        node := node
      }
    else none
  | ["", "sys", "liveness", "evictions"] =>
    some {
      family := .evictionBudget
      tenant := ""
      object := emptyObject
      source := emptyObject
      identityHash := ""
      version := ""
      node := ""
    }
  | _ => none

end Nayutar.Keyspace
