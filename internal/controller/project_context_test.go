package controller

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProjectContextAndImmutableContributions(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.WriteNote("demo", "project", "Canonical project description", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteNote("demo", "context", "Current project context", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkRepository("demo", "example/research"); err != nil {
		t.Fatal(err)
	}
	source, err := db.ArtifactSave("demo", "paper.pdf", []byte("source document"))
	if err != nil {
		t.Fatal(err)
	}
	p := ContributionRequest{
		Kind: "handoff", Title: "Proof engine handoff",
		Body:       "Verified release at revision 001; run the next theorem test",
		Repository: "example/research", Revision: strings.Repeat("a", 40),
		ArtifactID: asString(source["id"]),
	}
	created, err := db.ContributeProject("demo", p, "controller-owner")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := db.ProjectContribution("demo", asString(created["id"]))
	if err != nil || asString(loaded["body"]) != p.Body ||
		asString(loaded["body_sha256"]) != checksum([]byte(p.Body)) ||
		asString(loaded["revision"]) != p.Revision {
		t.Fatalf("immutable contribution mismatch: %v %v", loaded, err)
	}
	if _, err := db.WriteNote("demo", "context", "changed", 0); err == nil {
		t.Fatal("canonical notes must retain their optimistic version guards")
	}
	decision, err := db.ContributeProject("demo", ContributionRequest{
		Kind: "decision", Title: "Use a constrained worker",
		Body: "Preserve scarce GPU capacity", Repository: "example/research",
		Revision: strings.Repeat("b", 40),
	}, "controller-owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := db.ProjectContext("demo")
	if err != nil {
		t.Fatal(err)
	}
	projDoc, _ := ctx["project_document"].(map[string]any)
	contextDoc, _ := ctx["context_document"].(map[string]any)
	latest, _ := ctx["latest_handoff"].(map[string]any)
	decisions, _ := ctx["recent_decisions"].([]map[string]any)
	if asString(projDoc["body"]) != "Canonical project description" ||
		asString(contextDoc["body"]) != "Current project context" ||
		asString(latest["id"]) != asString(created["id"]) ||
		len(decisions) != 1 || asString(decisions[0]["id"]) != asString(decision["id"]) {
		t.Fatalf("canonical project context incomplete: %v", ctx)
	}
	found, err := db.ProjectContributionSearch("demo", "scarce GPU")
	if err != nil || len(found) != 1 || asString(found[0]["kind"]) != "decision" {
		t.Fatalf("contribution search: %v %v", found, err)
	}
	if _, err := db.ContributeProject("demo", ContributionRequest{
		Kind: "finding", Title: "Wrong revision", Body: "Invalid", Repository: "example/research", Revision: "main",
	}, "controller-owner"); err == nil {
		t.Fatal("moving git branch is not immutable source provenance")
	}
	if _, err := db.ContributeProject("demo", ContributionRequest{
		Kind: "finding", Title: "Unlinked source", Body: "Invalid", Repository: "other/repository", Revision: strings.Repeat("c", 40),
	}, "controller-owner"); err == nil {
		t.Fatal("contribution may not reference unlinked repositories")
	}
	if _, err := db.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProjectContribution("other", asString(created["id"])); err == nil {
		t.Fatal("cross-project contribution retrieval must fail")
	}
	if _, err := db.ContributeProject("other", ContributionRequest{
		Kind: "finding", Title: "Foreign artifact", Body: "Invalid", ArtifactID: asString(source["id"]),
	}, "controller-owner"); err == nil {
		t.Fatal("foreign source artifacts must not be accepted")
	}
	if _, err := db.ContributeProject("demo", ContributionRequest{
		Kind: "finding", Title: "Forged user", Body: "Invalid",
	}, "unverified-principal"); err == nil {
		t.Fatal("unverified authorship must be rejected")
	}
	// Existing immutable contributions are integrity checked at read time.
	if err := db.exec("UPDATE project_contributions SET body=? WHERE id=?", "tampered", asString(created["id"])); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProjectContribution("demo", asString(created["id"])); err == nil {
		t.Fatal("contribution checksum mismatch must be rejected")
	}
}

func TestProjectKnowledgeRESTAndMCPIdentity(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	principal := "oidc:" + issuer + "#researcher"
	if _, err := db.SetProjectGrant("demo", principal, "editor", ""); err != nil {
		t.Fatal(err)
	}
	editor := signedTestToken(t, key, issuer, "project-workspace", "researcher", time.Now().Add(time.Hour).Unix())
	code, _ := call(t, httpServer, http.MethodPost, "/api/projects/demo/knowledge/contributions", editor, ContributionRequest{
		Kind: "finding", Title: "New result", Body: "Result with fixed scope",
	})
	if code != http.StatusConflict {
		t.Fatal("REST accepted a non-Git knowledge write")
	}
	created, err := db.ContributeProject("demo", ContributionRequest{
		Kind: "finding", Title: "Historical result", Body: "Result with fixed scope",
	}, principal)
	if err != nil {
		t.Fatal(err)
	}
	if created["principal"] != principal {
		t.Fatalf("REST must bind authenticated user, got %v", created)
	}
	code, read := call(t, httpServer, http.MethodGet, "/api/projects/demo/knowledge/contributions/"+asString(created["id"]), editor, nil)
	wantOK(t, code, read)
	if read["body"] != "Result with fixed scope" {
		t.Fatalf("could not read immutable contribution: %v", read)
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/projects/other/knowledge/contributions", editor, ContributionRequest{
		Kind: "finding", Title: "Foreign", Body: "Not authorized",
	})
	if code != http.StatusForbidden {
		t.Fatal("cross-project REST write must fail")
	}
	code, rpc := call(t, httpServer, http.MethodPost, "/mcp/", editor, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "knowledge_contribute", "arguments": map[string]any{
				"project_id": "demo", "kind": "handoff", "title": "Remote handoff", "body": "Continue on verified revision",
				"principal": "forged-owner",
			},
		},
	})
	wantOK(t, code, rpc)
	result, _ := rpc["result"].(map[string]any)
	if !asBool(result["isError"]) {
		t.Fatalf("MCP accepted a local knowledge contribution: %v", result)
	}
	latest, err := db.ProjectContext("demo")
	if err != nil {
		t.Fatal(err)
	}
	if latest["latest_handoff"] != nil {
		t.Fatal("rejected contribution created a handoff")
	}
	code, _ = call(t, httpServer, http.MethodGet, "/api/projects/other/knowledge/context", editor, nil)
	if code != http.StatusForbidden {
		t.Fatal("cross-project REST read must fail")
	}
	if _, err := db.SetProjectGrant("demo", principal, "viewer", "editor"); err != nil {
		t.Fatal(err)
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/projects/demo/knowledge/contributions", editor, ContributionRequest{
		Kind: "finding", Title: "Viewer cannot edit", Body: "Denied",
	})
	if code != http.StatusForbidden {
		t.Fatal("viewer contribution must be denied")
	}
	code, context := call(t, httpServer, http.MethodGet, "/api/projects/demo/knowledge/context", editor, nil)
	wantOK(t, code, context)
	if context["authority"] != "controller-local" {
		t.Fatalf("canonical authority must be explicit: %v", context)
	}
}
