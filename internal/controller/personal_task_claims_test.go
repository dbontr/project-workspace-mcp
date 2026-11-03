package controller

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonalClaimFencingAndDurableResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Research unit", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, key := asString(task["id"]), "study-derivatives"
	first, err := db.ClaimPersonalTask("controller-owner", id, key, 60)
	if err != nil || first["status"] != "claimed" || asInt(first["generation"]) != 1 {
		t.Fatalf("first lease: %v %v", first, err)
	}
	token := asString(first["lease_token"])
	second, err := db.ClaimPersonalTask("controller-owner", id, key, 60)
	if err != nil || second["status"] != "busy" || second["lease_token"] != nil {
		t.Fatalf("other worker must not receive capability token: %v %v", second, err)
	}
	if _, err := db.RenewPersonalTaskClaim("controller-owner", id, key, "wrong", 1, 60); err == nil {
		t.Fatal("wrong credential must not renew claim")
	}
	if _, err := db.FinishPersonalTaskClaim("controller-owner", id, key, token, "completed", 1, []string{strings.Repeat("a", 32)}); err == nil {
		t.Fatal("cannot complete with foreign or unknown artifact ID")
	}
	if err := db.exec("UPDATE personal_task_claims SET expires=? WHERE task=? AND work_key=?", stamp()-5, id, key); err != nil {
		t.Fatal(err)
	}
	expired, err := db.PersonalTaskClaimStatus("controller-owner", id, key)
	if err != nil || expired["status"] != "expired" {
		t.Fatalf("expiry status: %v %v", expired, err)
	}
	claimed, err := db.ClaimPersonalTask("controller-owner", id, key, 60)
	if err != nil || asInt(claimed["generation"]) != 2 {
		t.Fatalf("new claimant must fence old generation: %v %v", claimed, err)
	}
	if _, err := db.RenewPersonalTaskClaim("controller-owner", id, key, token, 1, 60); err == nil {
		t.Fatal("expired claimant may not renew successor lease")
	}
	if _, err := db.FinishPersonalTaskClaim("controller-owner", id, key, token, "completed", 1, nil); err == nil {
		t.Fatal("expired claimant may not finish successor lease")
	}
	file, err := db.SavePersonalTaskText("controller-owner", id, "result.txt", "verified output", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishPersonalTaskClaim("controller-owner", id, key, asString(claimed["lease_token"]), "completed", 2,
		[]string{asString(file["artifact_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewPersonalTaskClaim("controller-owner", id, key, asString(claimed["lease_token"]), 2, 60); err == nil {
		t.Fatal("completed claim cannot be renewed")
	}
	complete, err := db.ClaimPersonalTask("controller-owner", id, key, 60)
	if err != nil || complete["status"] != "completed" || complete["lease_token"] != nil {
		t.Fatalf("completed work should be reused: %v %v", complete, err)
	}
	results, ok := complete["results"].([]string)
	if !ok || len(results) != 1 || results[0] != file["artifact_id"] {
		t.Fatalf("durable result file ID missing: %v", complete)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.PersonalTaskClaimStatus("controller-owner", id, key)
	if err != nil || saved["status"] != "completed" {
		t.Fatalf("completed claim not persisted: %v %v", saved, err)
	}
	if _, err := db.PersonalTaskClaimStatus("oidc:https://identity.example#other", id, key); err == nil {
		t.Fatal("cross-owner claim lookup must fail")
	}
}

func TestPersonalClaimsOwnershipAndRelease(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	key, issuer := testOIDCIssuer(t, server)
	alice := signedTestToken(t, key, issuer, "project-workspace", "alice", time.Now().Add(time.Hour).Unix())
	bob := signedTestToken(t, key, issuer, "project-workspace", "bob", time.Now().Add(time.Hour).Unix())
	code, created := call(t, httpServer, http.MethodPost, "/api/tasks", alice,
		map[string]any{"title": "Notebook"})
	wantOK(t, code, created)
	id := asString(created["id"])
	code, claim := call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims", alice,
		map[string]any{"work_key": "search-sha256", "lease_seconds": 60})
	wantOK(t, code, claim)
	if claim["status"] != "claimed" {
		t.Fatalf("REST personal claim: %v", claim)
	}
	code, _ = call(t, httpServer, http.MethodGet, "/api/tasks/"+id+"/claims", bob, nil)
	if code == http.StatusOK {
		t.Fatal("different principal cannot inspect claims")
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims", bob,
		map[string]any{"work_key": "search-sha256"})
	if code == http.StatusOK {
		t.Fatal("different principal cannot claim another person's task")
	}
	code, renewed := call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims/search-sha256/renew", alice,
		map[string]any{"lease_token": claim["lease_token"], "generation": 1, "lease_seconds": 60})
	wantOK(t, code, renewed)
	if renewed["status"] != "claimed" {
		t.Fatalf("renew: %v", renewed)
	}
	code, released := call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims/search-sha256/finish", alice,
		map[string]any{"lease_token": claim["lease_token"], "generation": 1, "status": "released"})
	wantOK(t, code, released)
	if released["status"] != "released" {
		t.Fatalf("release: %v", released)
	}
	code, again := call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims", alice,
		map[string]any{"work_key": "search-sha256"})
	wantOK(t, code, again)
	if again["status"] != "claimed" || asInt(again["generation"]) != 2 {
		t.Fatalf("released claim must be reclaimable with a higher generation: %v", again)
	}
	// Results from another task are rejected, even when both tasks have
	// the same authenticated owner.
	other, err := db.CreatePersonalTask("oidc:"+issuer+"#alice", "Different task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := db.SavePersonalTaskText("oidc:"+issuer+"#alice", asString(other["id"]), "different.txt", "foreign", 0)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/claims/search-sha256/finish", alice,
		map[string]any{"lease_token": again["lease_token"], "generation": 2, "status": "completed",
			"artifact_ids": []string{asString(source["artifact_id"])}})
	if code == http.StatusOK {
		t.Fatal("claim cannot reuse unrelated personal task's files")
	}
	// MCP rejects ownership spoofing and lists only the caller's claims.
	code, response := call(t, httpServer, http.MethodPost, "/mcp/", bob,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "personal_task_claim_status", "arguments": map[string]any{
				"task_id": id, "work_key": "search-sha256", "owner": "oidc:" + issuer + "#alice",
			}}})
	wantOK(t, code, response)
	rpc, _ := response["result"].(map[string]any)
	if !asBool(rpc["isError"]) {
		t.Fatal("MCP may not accept caller-controlled owner")
	}
	otherClaims, err := db.PersonalTaskClaimList("oidc:"+issuer+"#bob", id)
	if err == nil || len(otherClaims) != 0 {
		t.Fatalf("foreign claim list was exposed: %v %v", otherClaims, err)
	}
}
