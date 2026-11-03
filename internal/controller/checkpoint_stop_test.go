package controller

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCheckpointStopPreservesSnapshotBeforeTeardown(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	workerToken := pairWorker(t, httpServer)
	code, created := call(t, httpServer, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512})
	wantOK(t, code, created)
	workspace := asString(created["id"])
	start := claimOp(t, httpServer, workerToken)
	finishOp(t, httpServer, workerToken, start, map[string]any{"status": "ready"})
	path := "/api/projects/demo/workspaces/" + workspace + "/checkpoint-stop"
	code, requested := call(t, httpServer, http.MethodPost, path, testOwner, nil)
	wantOK(t, code, requested)
	if requested["status"] != "pending" {
		t.Fatalf("checkpoint did not queue: %v", requested)
	}
	code, repeated := call(t, httpServer, http.MethodPost, path, testOwner, nil)
	wantOK(t, code, repeated)
	if repeated["snapshot_operation"] != requested["snapshot_operation"] {
		t.Fatalf("duplicate request created another snapshot: %v", repeated)
	}
	code, _ = call(t, httpServer, http.MethodPost,
		"/api/projects/demo/workspaces/"+workspace+"/stop", testOwner, nil)
	if code == http.StatusOK {
		t.Fatal("direct stop cannot bypass a pending checkpoint")
	}
	code, _ = call(t, httpServer, http.MethodPost,
		"/api/projects/demo/workspaces/"+workspace+"/operations", testOwner,
		map[string]any{"action": "write_file", "payload": map[string]any{"path": "late.txt", "content": "late"}})
	if code == http.StatusOK {
		t.Fatal("workspace writes must be quiesced during checkpoint")
	}
	snapshot := claimOp(t, httpServer, workerToken)
	if snapshot["id"] != requested["snapshot_operation"] || snapshot["action"] != "snapshot" {
		t.Fatalf("unexpected snapshot operation: %v", snapshot)
	}
	// Controller accepts inline snapshots only after the exact leased worker
	// completes its operation and creates an immutable project artifact.
	payload := []byte("complete checkpoint bytes")
	finishOp(t, httpServer, workerToken, snapshot, map[string]any{
		"name": "workspace.tar.gz", "base64": base64.StdEncoding.EncodeToString(payload),
	})
	code, checkpoint := call(t, httpServer, http.MethodGet, path, testOwner, nil)
	wantOK(t, code, checkpoint)
	if checkpoint["status"] != "stopping" || checkpoint["snapshot_id"] == "" ||
		checkpoint["stop_operation"] == "" {
		t.Fatalf("stop was not gated on verified snapshot: %v", checkpoint)
	}
	all, err := db.SnapshotList("demo", workspace)
	if err != nil || len(all) != 1 ||
		all[0]["id"] != checkpoint["snapshot_id"] {
		t.Fatalf("checkpoint snapshot missing from durable library: %v %v", all, err)
	}
	artifact, err := db.ArtifactInfo("demo", asString(all[0]["artifact"]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.VerifyArtifact(artifact); err != nil {
		t.Fatalf("checkpoint snapshot checksum failed: %v", err)
	}
	operation := claimOp(t, httpServer, workerToken)
	if operation["action"] != "stop" || operation["id"] != checkpoint["stop_operation"] {
		t.Fatalf("unexpected teardown operation: %v", operation)
	}
	finishOp(t, httpServer, workerToken, operation, map[string]any{"removed": true})
	code, final := call(t, httpServer, http.MethodGet, path, testOwner, nil)
	wantOK(t, code, final)
	if final["status"] != "stopped" {
		t.Fatalf("checkpoint teardown did not complete: %v", final)
	}
	// A checkpoint remains usable on a replacement worker after teardown.
	code, replacement := call(t, httpServer, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512})
	wantOK(t, code, replacement)
	startReplacement := claimOp(t, httpServer, workerToken)
	finishOp(t, httpServer, workerToken, startReplacement, map[string]any{"status": "ready"})
	recovered, err := db.RestoreSnapshot("demo", asString(replacement["id"]), asString(checkpoint["snapshot_id"]))
	if err != nil || recovered["status"] != "queued" {
		t.Fatalf("replacement workspace cannot restore same-project snapshot: %v %v", recovered, err)
	}
	_ = server
}

func TestCheckpointStopFailureKeepsWorkspaceIntact(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	workerToken := pairWorker(t, httpServer)
	code, created := call(t, httpServer, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo"})
	wantOK(t, code, created)
	ws := asString(created["id"])
	finishOp(t, httpServer, workerToken, claimOp(t, httpServer, workerToken), map[string]any{"status": "ready"})
	first, err := db.CheckpointStopWorkspace("demo", ws)
	if err != nil {
		t.Fatal(err)
	}
	op := claimOp(t, httpServer, workerToken)
	code, failed := call(t, httpServer, http.MethodPost, "/api/worker/worker-one/operation/finish", workerToken,
		map[string]any{"operation_id": op["id"], "lease_token": op["lease_token"],
			"success": false, "result": map[string]any{"error": "disk full"}})
	wantOK(t, code, failed)
	after, err := db.CheckpointStopStatus("demo", ws)
	if err != nil || after["status"] != "failed" {
		t.Fatalf("failure must stop teardown sequence: %v %v", after, err)
	}
	current, err := db.Workspace("demo", ws)
	if err != nil || current["status"] != "ready" {
		t.Fatalf("workspace must remain ready after failed checkpoint: %v %v", current, err)
	}
	rows, err := db.SnapshotList("demo", ws)
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed checkpoint must not produce a retained snapshot: %v %v", rows, err)
	}
	retry, err := db.CheckpointStopWorkspace("demo", ws)
	if err != nil || retry["status"] != "pending" ||
		retry["snapshot_operation"] == first["snapshot_operation"] {
		t.Fatalf("failed checkpoint was not retryable: %v %v", retry, err)
	}
}

func TestCheckpointStopExpiredWorkerLeaseCannotTriggerTeardown(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	workerToken := pairWorker(t, httpServer)
	code, created := call(t, httpServer, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo"})
	wantOK(t, code, created)
	ws := asString(created["id"])
	finishOp(t, httpServer, workerToken, claimOp(t, httpServer, workerToken), map[string]any{"status": "ready"})
	requested, err := db.CheckpointStopWorkspace("demo", ws)
	if err != nil {
		t.Fatal(err)
	}
	snap := claimOp(t, httpServer, workerToken)
	if err := db.exec("UPDATE operations SET lease_until=? WHERE id=?", stamp()-1, snap["id"]); err != nil {
		t.Fatal(err)
	}
	_, err = db.ClaimOperation("worker-one")
	if err != nil {
		t.Fatal(err)
	}
	status, err := db.CheckpointStopStatus("demo", ws)
	if err != nil || status["status"] != "failed" {
		t.Fatalf("expired operation must abandon teardown: %v %v", status, err)
	}
	fake, err := db.FinishOperation("worker-one", asString(snap["id"]),
		asString(snap["lease_token"]), true, map[string]any{
			"name": "snapshot.tar.gz", "base64": base64.StdEncoding.EncodeToString([]byte("late")),
		})
	if err == nil {
		t.Fatalf("late claimant completed expired checkpoint: %v", fake)
	}
	workspace, err := db.Workspace("demo", ws)
	if err != nil || workspace["status"] != "ready" {
		t.Fatalf("expired worker operation must not stop workspace: %v %v", workspace, err)
	}
	if _, err := db.CheckpointStopWorkspace("demo", ws); err != nil {
		t.Fatal(err)
	}
	_ = requested
}

func TestCheckpointStopRespectsProjectGrants(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	workerToken := pairWorker(t, httpServer)
	code, created := call(t, httpServer, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo"})
	wantOK(t, code, created)
	ws := asString(created["id"])
	finishOp(t, httpServer, workerToken, claimOp(t, httpServer, workerToken), map[string]any{"status": "ready"})
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace", "viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/demo/workspaces/" + ws + "/checkpoint-stop"
	code, _ = call(t, httpServer, http.MethodPost, path, viewer, nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewers may not initiate teardown: %d", code)
	}
	if _, err := db.CheckpointStopWorkspace("demo", ws); err != nil {
		t.Fatal(err)
	}
	code, result := call(t, httpServer, http.MethodGet, path, viewer, nil)
	wantOK(t, code, result)
	if result["status"] != "pending" {
		t.Fatalf("viewer cannot inspect checkpoint progress: %v", result)
	}
	if _, err := db.CheckpointStopStatus("other", ws); err == nil {
		t.Fatal("cross-project checkpoint status should be inaccessible")
	}
	_, err := db.CallWorkspace("demo", ws, "command", map[string]any{"command": strings.Repeat("x", 5)})
	if err == nil {
		t.Fatal("checkpoint must freeze further operations")
	}
}
