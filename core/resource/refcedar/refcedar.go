// Package refcedar implements refs.Authorizer using cedar-go.
package refcedar

import (
	"fmt"

	"github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"

	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	actionEntityType = cedar.EntityType("Nayutar::Action")
	actionID         = cedar.String("Reference")
	objectEntityType = cedar.EntityType("Nayutar::Object")
)

var referenceActionUID = cedar.NewEntityUID(actionEntityType, actionID)

// Authorizer evaluates reference authorization requests against a Cedar policy set and entity store.
type Authorizer struct {
	policies *cedar.PolicySet
	entities cedar.EntityMap
}

var _ refs.Authorizer = (*Authorizer)(nil)

// New returns an Authorizer configured with policies and entities.
// A nil policies argument is treated as an empty policy set.
// It returns an error if entities contains any entity of type Nayutar::Object,
// or references Nayutar::Object in parents, attributes, or tags.
func New(policies *cedar.PolicySet, entities cedar.EntityMap) (*Authorizer, error) {
	for uid, ent := range entities {
		if uid.Type == objectEntityType || ent.UID.Type == objectEntityType {
			return nil, fmt.Errorf("refcedar: entities must not contain %s (%s)", objectEntityType, uid)
		}
		for parent := range ent.Parents.All() {
			if parent.Type == objectEntityType {
				return nil, fmt.Errorf("refcedar: entity %s parents must not contain %s (%s)", uid, objectEntityType, parent)
			}
		}
		if bad, ok := findObjectUID(ent.Attributes); ok {
			return nil, fmt.Errorf("refcedar: entity %s attributes must not contain %s (%s)", uid, objectEntityType, bad)
		}
		if bad, ok := findObjectUID(ent.Tags); ok {
			return nil, fmt.Errorf("refcedar: entity %s tags must not contain %s (%s)", uid, objectEntityType, bad)
		}
	}
	if policies == nil {
		policies = cedar.NewPolicySet()
	}
	return &Authorizer{
		policies: policies,
		entities: entities.Clone(),
	}, nil
}

func findObjectUID(v cedar.Value) (cedar.EntityUID, bool) {
	switch x := v.(type) {
	case cedar.EntityUID:
		if x.Type == objectEntityType {
			return x, true
		}
	case *cedar.EntityUID:
		if x != nil && x.Type == objectEntityType {
			return *x, true
		}
	case cedar.Record:
		for _, elem := range x.All() {
			if bad, ok := findObjectUID(elem); ok {
				return bad, true
			}
		}
	case *cedar.Record:
		if x != nil {
			for _, elem := range x.All() {
				if bad, ok := findObjectUID(elem); ok {
					return bad, true
				}
			}
		}
	case cedar.Set:
		for elem := range x.All() {
			if bad, ok := findObjectUID(elem); ok {
				return bad, true
			}
		}
	case *cedar.Set:
		if x != nil {
			for elem := range x.All() {
				if bad, ok := findObjectUID(elem); ok {
					return bad, true
				}
			}
		}
	}
	return cedar.EntityUID{}, false
}

// Authorize reports whether req is allowed by the configured Cedar policies and entities.
func (a *Authorizer) Authorize(req refs.Request) bool {
	if a == nil || req.Principal.Type == "" || req.Principal.ID == "" {
		return false
	}
	if err := keyspace.ValidateTenant(req.Tenant); err != nil {
		return false
	}
	if err := keyspace.ValidateName(req.Source.Name); err != nil {
		return false
	}
	if err := keyspace.ValidateName(req.Target.Name); err != nil {
		return false
	}
	targetQKind, err := keyspace.QualifiedKind(req.Target.Group, req.Target.Kind)
	if err != nil {
		return false
	}
	sourceQKind, err := keyspace.QualifiedKind(req.Source.Group, req.Source.Kind)
	if err != nil {
		return false
	}

	resourceUID := cedar.NewEntityUID(
		objectEntityType,
		cedar.String(req.Tenant+"/"+targetQKind+"/"+req.Target.Name),
	)

	cedarReq := cedar.Request{
		Principal: cedar.NewEntityUID(cedar.EntityType(req.Principal.Type), cedar.String(req.Principal.ID)),
		Action:    referenceActionUID,
		Resource:  resourceUID,
		Context: cedar.NewRecord(cedar.RecordMap{
			"tenant":     cedar.String(req.Tenant),
			"sourceKind": cedar.String(sourceQKind),
			"sourceName": cedar.String(req.Source.Name),
			"sourcePath": cedar.String(req.SourcePath),
		}),
	}

	var entities types.EntityGetter = a.entities
	if req.TargetExists {
		entities = requestEntities{
			base: a.entities,
			resource: cedar.Entity{
				UID:     resourceUID,
				Parents: cedar.NewEntityUIDSet(),
				Attributes: cedar.NewRecord(cedar.RecordMap{
					"tenant": cedar.String(req.Tenant),
					"kind":   cedar.String(targetQKind),
					"name":   cedar.String(req.Target.Name),
					"labels": labelsRecord(req.TargetLabels),
				}),
			},
		}
	}

	decision, diag := cedar.Authorize(a.policies, entities, cedarReq)
	// cedar-go skips policies that error during evaluation, so treat any evaluation error as deny to fail closed.
	return decision == cedar.Allow && len(diag.Errors) == 0
}

type requestEntities struct {
	base     cedar.EntityMap
	resource cedar.Entity
}

func (e requestEntities) Get(uid cedar.EntityUID) (cedar.Entity, bool) {
	if uid == e.resource.UID {
		return e.resource, true
	}
	return e.base.Get(uid)
}

func labelsRecord(labels map[string]string) cedar.Record {
	if len(labels) == 0 {
		return cedar.NewRecord(nil)
	}
	m := make(cedar.RecordMap, len(labels))
	for k, v := range labels {
		m[cedar.String(k)] = cedar.String(v)
	}
	return cedar.NewRecord(m)
}
