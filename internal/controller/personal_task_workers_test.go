package controller

import "testing"

func TestStandaloneTaskWorkersRequireAdminApproval(t *testing.T) {
	db, _, srv := fixture(t)
	pairWorker(t, srv)
	if _, err := db.RegisterNode("unapproved-worker", caps()); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Independent compilation", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(task["id"])
	submit := func(image string) error {
		_, err := db.SubmitPersonalTaskJob("controller-owner", id, "echo ok", image, 1, 256)
		return err
	}
	if err := submit("debian:bookworm-slim"); err == nil {
		t.Fatal("unapproved workers were accepted for standalone tasks")
	}
	if _, err := db.AuthorizePersonalTaskWorker("worker-one", true, true); err != nil {
		t.Fatal(err)
	}
	if err := submit("unapproved:image"); err == nil {
		t.Fatal("task submitted with image absent from approved workers")
	}
	if err := submit("debian:bookworm-slim"); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimJob("unapproved-worker")
	if err != nil || job != nil {
		t.Fatalf("unapproved worker claimed personal task: %v %v", job, err)
	}
	job, err = db.ClaimJob("worker-one")
	if err != nil || job == nil || asString(job["project"]) != personalComputeScope {
		t.Fatalf("approved worker could not claim task: %v %v", job, err)
	}
	if _, err := db.RevokeNode("worker-one"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.PersonalTaskWorkerAccess()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["node"] == "worker-one" && asInt(row["allowed"]) != 0 {
			t.Fatalf("revoked worker retained standalone execution permission: %v", row)
		}
	}
}

func TestStandaloneTaskWorkerAuthorizationToolIsAdministratorOnly(t *testing.T) {
	if !rootTools["task_worker_authorize"] || !rootTools["task_worker_access"] {
		t.Fatal("standalone worker administration is not restricted to controller owner")
	}
}
