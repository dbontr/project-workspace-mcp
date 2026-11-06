package controller

import "testing"

func TestTaskHardwareLeasesAreExclusiveWithProjectWorkspaces(t *testing.T) {
	db, _, srv := fixture(t)
	pairWorker(t, srv)
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, true); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "EDA synthesis", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(task["id"])
	resources := map[string]any{"npu": float64(1)}
	submit := func() map[string]any {
		t.Helper()
		result, err := db.SubmitPersonalTaskJobWithHardware("controller-owner",
			id, "echo synthesized", "debian:bookworm-slim", 1, 512, resources, 0)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := submit()
	if first["project_knowledge"] != false {
		t.Fatal("independent job created implicit knowledge")
	}
	firstJob, err := db.ClaimJob("worker-one")
	if err != nil || firstJob == nil || firstJob["project"] != personalComputeScope {
		t.Fatalf("first hardware job not claimable: %v %v", firstJob, err)
	}
	devices, ok := firstJob["devices"].([]map[string]any)
	if !ok || len(devices) != 1 || devices[0]["id"] != "npu-one" {
		t.Fatalf("task device lease was not delivered to worker: %v", firstJob["devices"])
	}
	_ = submit()
	if second, err := db.ClaimJob("worker-one"); err != nil || second != nil {
		t.Fatalf("device was double-allocated: %v %v", second, err)
	}
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "research", Hardware: resources, NodeID: "worker-one",
	}); err == nil {
		t.Fatal("workspace reused a leased task device")
	}
	if _, err := db.FinishJob("worker-one", asString(firstJob["id"]), asString(firstJob["lease_token"]),
		"succeeded", "completed"); err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimJob("worker-one")
	if err != nil || second == nil || second["devices"] == nil {
		t.Fatalf("reclaimed device not available: %v %v", second, err)
	}
	if _, err := db.FinishJob("worker-one", asString(second["id"]), asString(second["lease_token"]),
		"succeeded", "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "research", Hardware: resources, NodeID: "worker-one",
	}); err != nil {
		t.Fatalf("hardware not available to project workspace after job release: %v", err)
	}
}

func TestFailedTaskHardwareRemainsUnavailableUntilVerifiedRecovery(t *testing.T) {
	db, _, srv := fixture(t)
	pairWorker(t, srv)
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, true); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Unsafe process cleanup", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(task["id"])
	for i := 0; i < 2; i++ {
		if _, err := db.SubmitPersonalTaskJobWithHardware("controller-owner",
			id, "echo task", "debian:bookworm-slim", 1, 256,
			map[string]any{"npu": float64(1)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.ClaimJob("worker-one")
	if err != nil || first == nil {
		t.Fatalf("claim failed: %v %v", first, err)
	}
	if _, err := db.FinishJob("worker-one", asString(first["id"]), asString(first["lease_token"]),
		"failed", "cleanup uncertain"); err != nil {
		t.Fatal(err)
	}
	if next, err := db.ClaimJob("worker-one"); err != nil || next != nil {
		t.Fatalf("uncertain failed job released device: %v %v", next, err)
	}
	if _, err := db.RecoverTaskHardware(asString(first["id"]), true, false); err == nil {
		t.Fatal("accepted a release without cleanup verification")
	}
	if _, err := db.RecoverTaskHardware(asString(first["id"]), true, true); err != nil {
		t.Fatal(err)
	}
	if next, err := db.ClaimJob("worker-one"); err != nil || next == nil {
		t.Fatalf("second task did not receive verified released device: %v %v", next, err)
	}
}

func TestTaskHardwareRejectsUnsupportedOrUnsatisfiedRequests(t *testing.T) {
	db, _, srv := fixture(t)
	pairWorker(t, srv)
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, true); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Safety tests", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, hardware := range []map[string]any{
		{"usb": float64(1)},
		{"gpu": float64(1)},
		{"npu": float64(0.5)},
	} {
		if _, err := db.SubmitPersonalTaskJobWithHardware("controller-owner",
			asString(task["id"]), "echo task", "debian:bookworm-slim", 1, 256,
			hardware, 0); err == nil {
			t.Fatalf("accepted unsafe hardware request: %v", hardware)
		}
	}
}
