package controller

import "testing"

func TestWorkspaceStopRetriesExpiredWorkerLeaseWithoutEarlyHardwareRelease(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	caps := kubeWorkerCaps("demo")
	caps["devices"] = []any{map[string]any{
		"id": "gpu-slot-0", "kind": "gpu", "memory_mb": 12288.0,
	}}
	if _, err := db.RegisterNode("worker-one", caps); err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "demo", Hardware: map[string]any{"gpu": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := asString(created["id"])
	start, err := db.ClaimOperation("worker-one")
	if err != nil || start == nil || start["action"] != "start" {
		t.Fatalf("expected start claim: %v %v", start, err)
	}
	if _, err := db.FinishOperation("worker-one", asString(start["id"]),
		asString(start["lease_token"]), true, map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	first, err := db.StopWorkspace("demo", id)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := db.StopWorkspace("demo", id)
	if err != nil || repeated["operation_id"] != first["operation_id"] {
		t.Fatalf("repeated stop duplicated active operation: %v %v", repeated, err)
	}
	stop, err := db.ClaimOperation("worker-one")
	if err != nil || stop == nil || stop["action"] != "stop" {
		t.Fatalf("expected stop claim: %v %v", stop, err)
	}
	if err := db.exec("UPDATE operations SET lease_until=? WHERE id=?",
		stamp()-1, stop["id"]); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("UPDATE workspaces SET updated=? WHERE id=?", stamp()-90, id); err != nil {
		t.Fatal(err)
	}
	next, err := db.ClaimOperation("worker-one")
	if err != nil || next == nil || next["action"] != "stop" || next["id"] == stop["id"] {
		t.Fatalf("expired stop was not retried: %v %v", next, err)
	}
	if _, err := db.FinishOperation("worker-one", asString(stop["id"]),
		asString(stop["lease_token"]), true, map[string]any{"status": "stopped"}); err == nil {
		t.Fatal("expired worker was allowed to confirm teardown")
	}
	pending, err := db.Workspace("demo", id)
	if err != nil || pending["status"] != "stopping" {
		t.Fatalf("workspace prematurely left stopping state: %v %v", pending, err)
	}
	busy, err := db.queryOne(
		"SELECT count(*) busy FROM hardware_leases WHERE workspace=? AND released IS NULL", id)
	if err != nil || asInt(busy["busy"]) != 1 {
		t.Fatalf("hardware released without verified cleanup: %v %v", busy, err)
	}
	if _, err := db.FinishOperation("worker-one", asString(next["id"]),
		asString(next["lease_token"]), true, map[string]any{"status": "stopped"}); err != nil {
		t.Fatal(err)
	}
	done, err := db.Workspace("demo", id)
	if err != nil || done["status"] != "stopped" {
		t.Fatalf("workspace stop was not confirmed: %v %v", done, err)
	}
	busy, err = db.queryOne(
		"SELECT count(*) busy FROM hardware_leases WHERE workspace=? AND released IS NULL", id)
	if err != nil || asInt(busy["busy"]) != 0 {
		t.Fatalf("verified stop failed to release device: %v %v", busy, err)
	}
}

func TestWorkspaceStopCanBeRetriedAfterFailure(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	if _, err := db.RegisterNode("worker-one", kubeWorkerCaps("demo")); err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	id := asString(created["id"])
	start, err := db.ClaimOperation("worker-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishOperation("worker-one", asString(start["id"]),
		asString(start["lease_token"]), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StopWorkspace("demo", id); err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimOperation("worker-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishOperation("worker-one", asString(first["id"]),
		asString(first["lease_token"]), false,
		map[string]any{"error": "Kubernetes API temporarily unavailable"}); err != nil {
		t.Fatal(err)
	}
	retry, err := db.StopWorkspace("demo", id)
	if err != nil || retry["status"] != "stopping" {
		t.Fatalf("failed stop cannot be retried: %v %v", retry, err)
	}
	next, err := db.ClaimOperation("worker-one")
	if err != nil || next == nil || next["action"] != "stop" ||
		next["id"] == first["id"] {
		t.Fatalf("stop retry missing: %v %v", next, err)
	}
}
