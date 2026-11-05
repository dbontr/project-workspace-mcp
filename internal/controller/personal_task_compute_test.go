package controller

import "testing"

func TestIndependentTaskComputeDoesNotCreateProjectOrKnowledge(t *testing.T) {
	db, _, srv := fixture(t)
	pairWorker(t, srv)
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, false); err == nil {
		t.Fatal("standalone worker allowed without explicit authorization confirmation")
	}
	task, err := db.CreatePersonalTask("controller-owner", "Compile test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID := asString(task["id"])
	if _, err := db.SubmitPersonalTaskJob("someone-else", taskID, "echo test",
		"debian:bookworm-slim", 1, 256); err == nil {
		t.Fatal("different task owner accepted")
	}
	if _, err := db.SubmitPersonalTaskJob("controller-owner", taskID, "echo test",
		"debian:bookworm-slim", 1, 256); err == nil {
		t.Fatal("standalone task accepted a worker without administrator approval")
	}
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SubmitPersonalTaskJob("controller-owner", taskID, "echo test",
		"debian:bookworm-slim", 1, 256); err != nil {
		t.Fatal(err)
	}
	projects, err := db.Projects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("standalone task created a project: %v %v", projects, err)
	}
	row, err := db.ClaimJob("worker-one")
	if err != nil || row == nil || row["project"] != personalComputeScope {
		t.Fatalf("job not claimable by worker: %v %v", row, err)
	}
	_, err = db.FinishJob("worker-one", asString(row["id"]),
		asString(row["lease_token"]), "succeeded", "completed")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := db.PersonalTaskJobs("controller-owner", taskID)
	if err != nil || len(jobs) != 1 || jobs[0]["status"] != "succeeded" {
		t.Fatalf("task result missing: %v %v", jobs, err)
	}
	if _, err := db.PersonalTaskJobs("another-person", taskID); err == nil {
		t.Fatal("task result exposed to another owner")
	}
	// Finished job output is short-lived operational state, not knowledge.
	if err := db.exec("UPDATE jobs SET updated=? WHERE id=?",
		stamp()-personalJobRetentionSeconds-1, row["id"]); err != nil {
		t.Fatal(err)
	}
	retained, err := db.PersonalTaskJobs("controller-owner", taskID)
	if err != nil || len(retained) != 0 {
		t.Fatalf("expired task result retained: %v %v", retained, err)
	}
	if _, err := db.CreateProject(personalComputeScope, "Impersonation"); err == nil {
		t.Fatal("reserved compute scope accepted as a user project")
	}
}
