package refcedar_test

import (
	"testing"

	"github.com/cedar-policy/cedar-go"

	"github.com/Kaikei-e/Nayutar/core/resource/refcedar"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func mustPolicySet(t *testing.T, src string) *cedar.PolicySet {
	t.Helper()
	ps, err := cedar.NewPolicySetFromBytes("test.cedar", []byte(src))
	if err != nil {
		t.Fatalf("NewPolicySetFromBytes failed: %v", err)
	}
	return ps
}

func mustAuthorizer(t *testing.T, policies *cedar.PolicySet, entities cedar.EntityMap) *refcedar.Authorizer {
	t.Helper()
	auth, err := refcedar.New(policies, entities)
	if err != nil {
		t.Fatalf("refcedar.New failed: %v", err)
	}
	return auth
}

func baseRequest() refs.Request {
	return refs.Request{
		Tenant: "acme",
		Principal: refs.Principal{
			Type: "Nayutar::User",
			ID:   "alice",
		},
		Source: keyspace.Object{
			Group: keyspace.CoreGroup,
			Kind:  "workload",
			Name:  "web",
		},
		SourcePath: "spec/secretRef",
		Target: keyspace.Object{
			Group: keyspace.CoreGroup,
			Kind:  "secret",
			Name:  "db",
		},
		TargetExists: true,
	}
}

func TestAuthorize(t *testing.T) {
	opsEntities := cedar.EntityMap{
		cedar.NewEntityUID("Nayutar::User", "alice"): {
			UID:     cedar.NewEntityUID("Nayutar::User", "alice"),
			Parents: cedar.NewEntityUIDSet(cedar.NewEntityUID("Nayutar::Group", "team-a")),
		},
		cedar.NewEntityUID("Nayutar::Group", "team-a"): {
			UID:     cedar.NewEntityUID("Nayutar::Group", "team-a"),
			Parents: cedar.NewEntityUIDSet(cedar.NewEntityUID("Nayutar::Group", "ops")),
		},
		cedar.NewEntityUID("Nayutar::User", "bob"): {
			UID:     cedar.NewEntityUID("Nayutar::User", "bob"),
			Parents: cedar.NewEntityUIDSet(),
		},
	}

	cases := []struct {
		name     string
		policies string
		nilPS    bool
		entities cedar.EntityMap
		mutate   func(*refs.Request)
		want     bool
	}{
		{
			name:     "permit by principal allowed",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);`,
			want:     true,
		},
		{
			name:     "permit by principal denied for other principal",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);`,
			mutate: func(r *refs.Request) {
				r.Principal.ID = "bob"
			},
			want: false,
		},
		{
			name:     "no policy denies",
			policies: "",
			want:     false,
		},
		{
			name:  "nil policy set denies",
			nilPS: true,
			want:  false,
		},
		{
			name: "forbid overrides permit",
			policies: `
				permit(principal, action == Nayutar::Action::"Reference", resource);
				forbid(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);
			`,
			want: false,
		},
		{
			name:     "label opt-in allows target with matching label",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource has labels && resource.labels has "share" && resource.labels["share"] == "yes" };`,
			mutate: func(r *refs.Request) {
				r.TargetLabels = map[string]string{"share": "yes"}
			},
			want: true,
		},
		{
			name:     "label opt-in denies target with wrong label value",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource has labels && resource.labels has "share" && resource.labels["share"] == "yes" };`,
			mutate: func(r *refs.Request) {
				r.TargetLabels = map[string]string{"share": "no"}
			},
			want: false,
		},
		{
			name:     "label opt-in denies target with nil labels",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource has labels && resource.labels has "share" };`,
			mutate: func(r *refs.Request) {
				r.TargetLabels = nil
			},
			want: false,
		},
		{
			name:     "label opt-in denies target with empty labels",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource has labels && resource.labels has "share" };`,
			mutate: func(r *refs.Request) {
				r.TargetLabels = map[string]string{}
			},
			want: false,
		},
		{
			name: "forbid with evaluation error denies even when permit matches",
			policies: `
				permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);
				forbid(principal, action == Nayutar::Action::"Reference", resource) when { resource.missingAttr == "x" };
			`,
			want: false,
		},
		{
			name: "permit with evaluation error denies even when another permit matches",
			policies: `
				permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);
				permit(principal, action == Nayutar::Action::"Reference", resource) when { resource.missingAttr == "x" };
			`,
			want: false,
		},
		{
			name:     "attribute-dependent policy on missing target denies with has check",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource has labels && resource.labels has "share" };`,
			mutate: func(r *refs.Request) {
				r.TargetExists = false
				r.TargetLabels = map[string]string{"share": "yes"}
			},
			want: false,
		},
		{
			name:     "attribute-dependent policy on missing target denies on direct attr access",
			policies: `permit(principal, action == Nayutar::Action::"Reference", resource) when { resource.tenant == "acme" };`,
			mutate: func(r *refs.Request) {
				r.TargetExists = false
			},
			want: false,
		},
		{
			name:     "resource pinned to tenant acme allows acme",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource == Nayutar::Object::"acme/secret/db");`,
			want:     true,
		},
		{
			name:     "resource pinned to tenant acme denies other tenant for same target",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource == Nayutar::Object::"acme/secret/db");`,
			mutate: func(r *refs.Request) {
				r.Tenant = "other"
			},
			want: false,
		},
		{
			name:     "non-core target qualified kind in resource UID and attributes",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource == Nayutar::Object::"acme/secret.vault.example.com/db") when { resource.kind == "secret.vault.example.com" && resource.name == "db" && resource.tenant == "acme" };`,
			mutate: func(r *refs.Request) {
				r.Target.Group = "vault.example.com"
			},
			want: true,
		},
		{
			name:     "context sourcePath and sourceKind match",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource) when { context.tenant == "acme" && context.sourceKind == "workload" && context.sourceName == "web" && context.sourcePath == "spec/secretRef" };`,
			want:     true,
		},
		{
			name:     "context non-core sourceKind matches",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource) when { context.sourceKind == "deployment.apps.example.com" && context.sourcePath == "spec/volumes" };`,
			mutate: func(r *refs.Request) {
				r.Source = keyspace.Object{Group: "apps.example.com", Kind: "deployment", Name: "api"}
				r.SourcePath = "spec/volumes"
			},
			want: true,
		},
		{
			name:     "context sourcePath mismatch denies",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource) when { context.sourceKind == "workload" && context.sourcePath == "spec/secretRef" };`,
			mutate: func(r *refs.Request) {
				r.SourcePath = "spec/otherRef"
			},
			want: false,
		},
		{
			name:     "context sourceKind mismatch denies",
			policies: `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource) when { context.sourceKind == "workload" && context.sourcePath == "spec/secretRef" };`,
			mutate: func(r *refs.Request) {
				r.Source.Kind = "job"
			},
			want: false,
		},
		{
			name:     "principal in parent group from configured entities allowed",
			policies: `permit(principal in Nayutar::Group::"ops", action == Nayutar::Action::"Reference", resource);`,
			entities: opsEntities,
			want:     true,
		},
		{
			name:     "principal not in parent group denied",
			policies: `permit(principal in Nayutar::Group::"ops", action == Nayutar::Action::"Reference", resource);`,
			entities: opsEntities,
			mutate: func(r *refs.Request) {
				r.Principal.ID = "bob"
			},
			want: false,
		},
		{
			name:     "empty principal type returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Principal.Type = ""
			},
			want: false,
		},
		{
			name:     "empty principal ID returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Principal.ID = ""
			},
			want: false,
		},
		{
			name:     "invalid target group returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Target.Group = ""
			},
			want: false,
		},
		{
			name:     "invalid source group returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Source.Group = ""
			},
			want: false,
		},
		{
			name:     "empty tenant returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Tenant = ""
			},
			want: false,
		},
		{
			name:     "invalid tenant returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Tenant = "acme/evil"
			},
			want: false,
		},
		{
			name:     "empty source name returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Source.Name = ""
			},
			want: false,
		},
		{
			name:     "invalid source name returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Source.Name = "Web"
			},
			want: false,
		},
		{
			name:     "empty target name returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Target.Name = ""
			},
			want: false,
		},
		{
			name:     "invalid target name returns false",
			policies: `permit(principal, action, resource);`,
			mutate: func(r *refs.Request) {
				r.Target.Name = "db/other"
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ps *cedar.PolicySet
			if !tc.nilPS {
				ps = mustPolicySet(t, tc.policies)
			}
			auth := mustAuthorizer(t, ps, tc.entities)
			req := baseRequest()
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			if got := auth.Authorize(req); got != tc.want {
				t.Fatalf("Authorize(%+v) = %v, want %v", req, got, tc.want)
			}
		})
	}
}

func TestNewRejectsObjectEntities(t *testing.T) {
	ps := mustPolicySet(t, `permit(principal, action, resource);`)

	cases := []struct {
		name     string
		entities cedar.EntityMap
		wantErr  bool
	}{
		{
			name:     "nil entities accepted",
			entities: nil,
			wantErr:  false,
		},
		{
			name: "user and group entities accepted",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID:     cedar.NewEntityUID("Nayutar::User", "alice"),
					Parents: cedar.NewEntityUIDSet(cedar.NewEntityUID("Nayutar::Group", "ops")),
				},
			},
			wantErr: false,
		},
		{
			name: "Nayutar::Object entity rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"): {
					UID: cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
				},
			},
			wantErr: true,
		},
		{
			name: "mismatched key with Nayutar::Object entity UID rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "spoof"): {
					UID: cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with Nayutar::Object parent rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Parents: cedar.NewEntityUIDSet(
						cedar.NewEntityUID("Nayutar::Group", "ops"),
						cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
					),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with Nayutar::Object in direct attribute rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Attributes: cedar.NewRecord(cedar.RecordMap{
						"target": cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
					}),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with Nayutar::Object in nested record attribute rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Attributes: cedar.NewRecord(cedar.RecordMap{
						"nested": cedar.NewRecord(cedar.RecordMap{
							"ref": cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
						}),
					}),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with Nayutar::Object in set inside record attribute rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Attributes: cedar.NewRecord(cedar.RecordMap{
						"items": cedar.NewSet(
							cedar.NewRecord(cedar.RecordMap{
								"obj": cedar.NewEntityUID("Nayutar::Object", "acme/secret/db"),
							}),
						),
					}),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with Nayutar::Object in tags rejected",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Tags: cedar.NewRecord(cedar.RecordMap{
						"tagRef": cedar.NewSet(cedar.NewEntityUID("Nayutar::Object", "acme/secret/db")),
					}),
				},
			},
			wantErr: true,
		},
		{
			name: "entity with non-object EntityUID in nested attributes and tags accepted",
			entities: cedar.EntityMap{
				cedar.NewEntityUID("Nayutar::User", "alice"): {
					UID: cedar.NewEntityUID("Nayutar::User", "alice"),
					Attributes: cedar.NewRecord(cedar.RecordMap{
						"team": cedar.NewEntityUID("Nayutar::Group", "ops"),
						"nested": cedar.NewRecord(cedar.RecordMap{
							"members": cedar.NewSet(cedar.NewEntityUID("Nayutar::User", "bob")),
						}),
					}),
					Tags: cedar.NewRecord(cedar.RecordMap{
						"owner": cedar.NewEntityUID("Nayutar::Group", "ops"),
					}),
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := refcedar.New(ps, tc.entities)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewRejectsPrincipalInObjectParent(t *testing.T) {
	ps := mustPolicySet(t, `permit (principal, action, resource) when { principal in resource };`)
	entities := cedar.EntityMap{
		cedar.NewEntityUID("Nayutar::User", "alice"): {
			UID:     cedar.NewEntityUID("Nayutar::User", "alice"),
			Parents: cedar.NewEntityUIDSet(cedar.NewEntityUID("Nayutar::Object", "acme/secret/db")),
		},
	}
	if _, err := refcedar.New(ps, entities); err == nil {
		t.Fatal("New() unexpectedly accepted principal with Nayutar::Object parent")
	}
}

func TestNewClonesEntities(t *testing.T) {
	ps := mustPolicySet(t, `permit(principal in Nayutar::Group::"ops", action == Nayutar::Action::"Reference", resource);`)
	aliceUID := cedar.NewEntityUID("Nayutar::User", "alice")
	entities := cedar.EntityMap{
		aliceUID: {
			UID:     aliceUID,
			Parents: cedar.NewEntityUIDSet(cedar.NewEntityUID("Nayutar::Group", "ops")),
		},
	}
	auth := mustAuthorizer(t, ps, entities)
	delete(entities, aliceUID)

	if !auth.Authorize(baseRequest()) {
		t.Fatal("Authorize() returned false after mutating caller EntityMap")
	}
}
