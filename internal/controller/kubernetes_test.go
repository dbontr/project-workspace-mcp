package controller

import "testing"

func kubeWorkerCaps(project string) map[string]any {
	return map[string]any{
		"cpu": 8.0, "memory_mb": 16384.0, "max_jobs": 4.0,
		"kubernetes": true, "docker": false, "project_id": project,
		"namespace": "pwm-" + project, "arch": "amd64",
		"images": []any{"debian:bookworm-slim"}, "devices": []any{},
	}
}

func TestKubernetesWorkersAreScopedToTheirProject(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	if _, err := db.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterNode("research-agent", kubeWorkerCaps("demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "other",
		Image: "debian:bookworm-slim"}); err == nil {
		t.Fatal("project scheduled onto another project's Kubernetes namespace")
	}
	plan, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "other",
		Image: "debian:bookworm-slim"})
	if err != nil || asInt(plan["eligible_count"]) != 0 {
		t.Fatalf("Kubernetes namespace planner ignored project isolation: %v %v", plan, err)
	}
	workspace, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo",
		Image: "debian:bookworm-slim"})
	if err != nil || workspace["node"] != "research-agent" {
		t.Fatalf("matching project could not use Kubernetes worker: %v %v", workspace, err)
	}
	if _, err := db.SubmitJob("other", "echo other", "debian:bookworm-slim", 1, 512); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SubmitJob("demo", "echo demo", "debian:bookworm-slim", 1, 512); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimJob("research-agent")
	if err != nil || job == nil || job["project"] != "demo" {
		t.Fatalf("job claim escaped namespace project: %v %v", job, err)
	}
}
