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

func TestKubernetesDeviceLeaseReachesPodStartPayload(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	caps := kubeWorkerCaps("demo")
	caps["devices"] = []any{
		map[string]any{"id": "gpu-slot-0", "kind": "gpu", "memory_mb": 16384.0},
		map[string]any{"id": "fpga-slot-0", "kind": "fpga"},
	}
	if _, err := db.RegisterNode("kube-hardware", caps); err != nil {
		t.Fatal(err)
	}
	req := WorkspaceRequest{ProjectID: "demo", Image: "debian:bookworm-slim",
		CPU: 1, MemoryMB: 1024, Hardware: map[string]any{"gpu": 1},
		MinGPUMemoryMB: 12288}
	plan, err := db.PlanWorkspace(req)
	if err != nil || asInt(plan["eligible_count"]) != 1 {
		t.Fatalf("GPU device-plugin profile was not eligible: %v %v", plan, err)
	}
	workspace, err := db.CreateWorkspace(req)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.queryOne("SELECT payload FROM operations WHERE workspace=? AND action='start'", workspace["id"])
	if err != nil || row == nil {
		t.Fatalf("workspace start payload missing: %v", err)
	}
	payload := decodeObject(asString(row["payload"]))
	devices, ok := payload["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("GPU hardware lease was not forwarded: %v", payload)
	}
	assigned := devices[0].(map[string]any)
	if assigned["id"] != "gpu-slot-0" || assigned["kind"] != "gpu" {
		t.Fatalf("incorrect Kubernetes device assignment: %v", assigned)
	}
	over := req
	over.MinGPUMemoryMB = 24576
	if _, err := db.CreateWorkspace(over); err == nil {
		t.Fatal("workspace accepted a GPU with insufficient declared VRAM")
	}
}
