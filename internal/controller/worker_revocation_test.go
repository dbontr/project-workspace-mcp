package controller

import (
	"net/http"
	"testing"
	"time"
)

func TestWorkerRevocationPreservesRecoveryState(t *testing.T) {
	db, server, srv := fixture(t)
	project(t, srv)
	token := pairWorker(t, srv)
	code, created := call(t, srv, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512,
			"hardware": map[string]any{"npu": 1}})
	wantOK(t, code, created)
	workspace := asString(created["id"])
	if workspace == "" || !db.NodeAuthorized("worker-one", token) {
		t.Fatal("worker enrollment did not establish a credential")
	}
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace",
		"viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	code, _ = call(t, srv, http.MethodPost, "/api/nodes/worker-one/revoke", viewer, nil)
	if code != http.StatusForbidden {
		t.Fatalf("non-administrator could revoke a worker: HTTP %d", code)
	}
	code, revoked := call(t, srv, http.MethodPost, "/api/nodes/worker-one/revoke", testOwner, nil)
	wantOK(t, code, revoked)
	if revoked["revoked"] != true ||
		asInt(revoked["active_workspaces"]) != 1 ||
		asInt(revoked["active_hardware_leases"]) != 1 ||
		asInt(revoked["unfinished_operations"]) != 1 {
		t.Fatalf("incomplete revocation recovery evidence: %v", revoked)
	}
	if db.NodeAuthorized("worker-one", token) {
		t.Fatal("revoked token still authenticates")
	}
	after, err := db.Workspace("demo", workspace)
	if err != nil || after["status"] != "starting" {
		t.Fatalf("revocation discarded the workspace: %v %v", after, err)
	}
	leases, err := db.query("SELECT workspace,released FROM hardware_leases WHERE workspace=?", workspace)
	if err != nil || len(leases) != 1 || leases[0]["released"] != nil {
		t.Fatalf("revocation silently released hardware: %v %v", leases, err)
	}
	operations, err := db.query("SELECT status FROM operations WHERE workspace=?", workspace)
	if err != nil || len(operations) != 1 || operations[0]["status"] != "queued" {
		t.Fatalf("revocation erased recoverable worker operations: %v %v", operations, err)
	}
	code, _ = call(t, srv, http.MethodPost, "/api/nodes/unknown/revoke", testOwner, nil)
	if code != http.StatusNotFound {
		t.Fatalf("nonexistent worker revocation should fail: HTTP %d", code)
	}
}

func TestWorkerRevocationMCPRequiresAdministrator(t *testing.T) {
	db, server, srv := fixture(t)
	project(t, srv)
	token := pairWorker(t, srv)
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace",
		"viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": "worker_revoke",
			"arguments": map[string]any{"node_id": "worker-one"}},
	}
	code, denied := call(t, srv, http.MethodPost, "/mcp", viewer, request)
	wantOK(t, code, denied)
	if gitObj(denied["result"])["isError"] != true || !db.NodeAuthorized("worker-one", token) {
		t.Fatalf("project viewer revoked a worker: %v", denied)
	}
	code, approved := call(t, srv, http.MethodPost, "/mcp", testOwner, request)
	wantOK(t, code, approved)
	if gitObj(approved["result"])["isError"] == true || db.NodeAuthorized("worker-one", token) {
		t.Fatalf("administrator MCP tool did not revoke worker: %v", approved)
	}
}
