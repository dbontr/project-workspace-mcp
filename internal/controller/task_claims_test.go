package controller

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestTaskClaimConcurrencyAndFencing(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	const clients = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winner map[string]any
	busy := 0
	errorsCount := 0
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := db.ClaimTask("research", "run-v1-sha256", 900)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorsCount++
				return
			}
			switch asString(result["status"]) {
			case "claimed":
				if winner != nil {
					errorsCount++
				}
				winner = result
			case "busy":
				busy++
			default:
				errorsCount++
			}
		}()
	}
	wg.Wait()
	if errorsCount != 0 || busy != clients-1 || winner == nil {
		t.Fatalf("claim race: errors=%d busy=%d winner=%v", errorsCount, busy, winner)
	}
	firstToken := asString(winner["lease_token"])
	if len(firstToken) != 64 {
		t.Fatal("missing fenced lease credential")
	}
	if _, err := db.FinishTaskClaim("research", "run-v1-sha256", "invalid", "completed", 1, nil); err == nil {
		t.Fatal("incorrect claim token must be rejected")
	}
	if _, err := db.RenewTaskClaim("research", "run-v1-sha256", firstToken, 1, 1800); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("UPDATE task_claims SET expires=? WHERE project='research' AND work_key='run-v1-sha256'", stamp()-1); err != nil {
		t.Fatal(err)
	}
	next, err := db.ClaimTask("research", "run-v1-sha256", 900)
	if err != nil || next["status"] != "claimed" || asInt(next["generation"]) != 2 {
		t.Fatalf("failed reclaim: %v %v", next, err)
	}
	if _, err := db.FinishTaskClaim("research", "run-v1-sha256", firstToken, "completed", 1, nil); err == nil {
		t.Fatal("stale task owner must not finish replacement claim")
	}
	artifact, err := db.ArtifactSave("research", "result.txt", []byte("verified"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.FinishTaskClaim("research", "run-v1-sha256", asString(next["lease_token"]), "completed", 2, []string{asString(artifact["id"])})
	if err != nil || out["status"] != "completed" {
		t.Fatalf("finish: %v %v", out, err)
	}
	cached, err := db.ClaimTask("research", "run-v1-sha256", 900)
	if err != nil || cached["status"] != "completed" {
		t.Fatalf("completed claims must not re-run: %v %v", cached, err)
	}
	if cached["lease_token"] != nil {
		t.Fatal("completed result must never expose a token")
	}
	claims, err := db.ListTaskClaims("research")
	if err != nil || len(claims) != 1 || claims[0]["token_hash"] != nil || claims[0]["lease_token"] != nil {
		t.Fatalf("listing must not disclose lease secrets: %v %v", claims, err)
	}
}

func TestTaskClaimProjectBoundariesAndRelease(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("first", "First"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("second", "Second"); err != nil {
		t.Fatal(err)
	}
	foreign, err := db.ArtifactSave("second", "data.bin", []byte("foreign"))
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := db.ClaimTask("first", "analysis", 900)
	if err != nil {
		t.Fatal(err)
	}
	token := asString(reserved["lease_token"])
	if _, err := db.FinishTaskClaim("first", "analysis", token, "completed", 1, []string{asString(foreign["id"])}); err == nil {
		t.Fatal("cannot claim foreign project artifact as output")
	}
	if _, err := db.FinishTaskClaim("first", "analysis", token, "released", 1, nil); err != nil {
		t.Fatal(err)
	}
	next, err := db.ClaimTask("first", "analysis", 900)
	if err != nil || asInt(next["generation"]) != 2 {
		t.Fatalf("release must allow a fenced reclaim: %v %v", next, err)
	}
}

func TestTaskClaimPersistsAfterDatabaseRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "state.db")
	db, err := OpenDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	reserved, err := db.ClaimTask("research", "source-sha256", 900)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	status, err := db.TaskClaimStatus("research", "source-sha256")
	if err != nil || status["status"] != "claimed" {
		t.Fatalf("lease must persist: %v %v", status, err)
	}
	if _, err := db.FinishTaskClaim("research", "source-sha256", asString(reserved["lease_token"]), "completed", 1, nil); err != nil {
		t.Fatal(err)
	}
	complete, err := db.TaskClaimStatus("research", "source-sha256")
	if err != nil || complete["status"] != "completed" {
		t.Fatalf("completion must persist: %v %v", complete, err)
	}
}

func TestTaskClaimsAPIAndMCP(t *testing.T) {
	_, server, httpServer := fixture(t)
	project(t, httpServer)
	code, _ := call(t, httpServer, "POST", "/api/projects/demo/claims", "", map[string]any{"work_key": "work-a"})
	if code != 401 {
		t.Fatal("claim creation must authenticate")
	}
	code, result := call(t, httpServer, "POST", "/api/projects/demo/claims", testOwner, map[string]any{"work_key": "work-a"})
	wantOK(t, code, result)
	if asString(result["status"]) != "claimed" || result["lease_token"] == nil {
		t.Fatalf("claim: %v", result)
	}
	code, state := call(t, httpServer, "GET", "/api/projects/demo/claims/work-a", testOwner, nil)
	wantOK(t, code, state)
	if state["lease_token"] != nil || state["status"] != "claimed" {
		t.Fatalf("status must not disclose secrets: %v", state)
	}
	resultMCP, err := server.callTool("task_claim", map[string]any{"project_id": "demo", "work_key": "work-a"})
	if err != nil || resultMCP.(map[string]any)["status"] != "busy" {
		t.Fatalf("MCP must honor the existing lease: %v %v", resultMCP, err)
	}
	code, done := call(t, httpServer, "POST", "/api/projects/demo/claims/work-a/finish", testOwner,
		map[string]any{"lease_token": result["lease_token"], "generation": result["generation"], "status": "completed"})
	wantOK(t, code, done)
	if asString(done["status"]) != "completed" {
		t.Fatalf("unexpected completed state: %v", done)
	}
	if _, err := server.callTool("task_claim", map[string]any{"project_id": "demo", "work_key": "work-a", "lease_seconds": 0}); err != nil {
		t.Fatal(err)
	}
}
