package controller

import (
	"strings"
	"testing"
	"time"
)

func plannerNode(cpu float64, memory float64, devices []any, image string) map[string]any {
	return map[string]any{
		"cpu": cpu, "memory_mb": memory, "max_jobs": 2, "docker": true,
		"arch": "amd64", "images": []any{image}, "devices": devices,
	}
}

func TestResourcePlanPrefersUnscarceCapacity(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	cpu := plannerNode(4, 4096, []any{}, "debian:bookworm-slim")
	gpu := plannerNode(16, 65536, []any{
		map[string]any{"id": "gpu-0", "kind": "gpu", "memory_mb": 16384, "model": "test-gpu"},
		map[string]any{"id": "npu-0", "kind": "npu", "model": "test-npu"},
	}, "debian:bookworm-slim")
	if _, err := db.RegisterNode("cpu-node", cpu); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterNode("gpu-node", gpu); err != nil {
		t.Fatal(err)
	}
	result, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", CPU: 2, MemoryMB: 2048})
	if err != nil || result["recommended_node"] != "cpu-node" || asInt(result["eligible_count"]) != 2 {
		t.Fatalf("CPU-only placement should avoid scarce GPUs: %v %v", result, err)
	}
	withGPU, err := db.PlanWorkspace(WorkspaceRequest{
		ProjectID: "demo", CPU: 2, MemoryMB: 2048, Hardware: map[string]any{"gpu": 1},
		MinGPUMemoryMB: 12000,
	})
	if err != nil || withGPU["recommended_node"] != "gpu-node" || asInt(withGPU["eligible_count"]) != 1 {
		t.Fatalf("GPU with sufficient VRAM required: %v %v", withGPU, err)
	}
	excess, err := db.PlanWorkspace(WorkspaceRequest{
		ProjectID: "demo", Hardware: map[string]any{"gpu": 1}, MinGPUMemoryMB: 20000,
	})
	if err != nil || excess["status"] != "no_match" || asInt(excess["eligible_count"]) != 0 {
		t.Fatalf("VRAM requirement cannot be met: %v %v", excess, err)
	}
	pinned, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", NodeID: "gpu-node"})
	if err != nil || pinned["recommended_node"] != "gpu-node" || asInt(pinned["eligible_count"]) != 1 {
		t.Fatalf("exact node request must restrict eligibility: %v %v", pinned, err)
	}
	if _, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", MinGPUMemoryMB: 1024}); err == nil {
		t.Fatal("VRAM requirement without GPU allocation must be rejected")
	}
	created, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "demo", NodeID: "gpu-node", Hardware: map[string]any{"gpu": 1},
		MinGPUMemoryMB: 12000,
	})
	if err != nil || created["node"] != "gpu-node" {
		t.Fatalf("scheduler must honor exact-node GPU policy: %v %v", created, err)
	}
	afterLease, err := db.PlanWorkspace(WorkspaceRequest{
		ProjectID: "demo", Hardware: map[string]any{"gpu": 1},
		MinGPUMemoryMB: 12000,
	})
	if err != nil || afterLease["status"] != "no_match" {
		t.Fatalf("exclusive GPU must be unavailable after lease: %v %v", afterLease, err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "demo", Hardware: map[string]any{"gpu": 1}, MinGPUMemoryMB: 20000,
	}); err == nil {
		t.Fatal("scheduler cannot bypass GPU VRAM requirements")
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", NodeID: "missing-worker"}); err == nil {
		t.Fatal("exact node must be enforced")
	}
}

func TestResourcePlanImageAndArchitecturePolicy(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.RegisterPoolImage("approved-image", "ubuntu:24.04", "Ubuntu", "amd64"); err != nil {
		t.Fatal(err)
	}
	caps := plannerNode(4, 8192, nil, "ubuntu:24.04")
	if _, err := db.RegisterNode("node-one", caps); err != nil {
		t.Fatal(err)
	}
	good, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "approved-image"})
	if err != nil || good["status"] != "candidates" {
		t.Fatalf("approved image must be schedulable: %v %v", good, err)
	}
	caps["images"] = []any{"debian:bookworm-slim"}
	if err := db.Heartbeat("node-one", caps); err != nil {
		t.Fatal(err)
	}
	missing, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "approved-image"})
	if err != nil || missing["status"] != "no_match" {
		t.Fatalf("worker image approval is required: %v %v", missing, err)
	}
	if _, err := db.SetPoolState("image", "approved-image", "retired", 1, "Security patch required"); err != nil {
		t.Fatal(err)
	}
	disabled, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "approved-image"})
	if err != nil || disabled["status"] != "no_match" {
		t.Fatalf("retired image cannot be planned: %v %v", disabled, err)
	}
	if !strings.Contains(strings.Join(disabled["blockers"].([]string), " "), "retired") {
		t.Fatalf("expected catalog policy explanation: %v", disabled)
	}
	arch, err := db.PlanWorkspace(WorkspaceRequest{ProjectID: "demo", Architecture: "arm64"})
	if err != nil || arch["status"] != "no_match" {
		t.Fatalf("requested architecture must constrain workers: %v %v", arch, err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", Architecture: "arm64"}); err == nil {
		t.Fatal("scheduler must deny incompatible architecture")
	}
}

func TestResourcePlanReadOnlyAndOfflineDiagnostics(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	offline := plannerNode(4, 4096, nil, "debian:bookworm-slim")
	if _, err := db.RegisterNode("offline-node", offline); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("UPDATE nodes SET last_seen=? WHERE id=?", stamp()-400, "offline-node"); err != nil {
		t.Fatal(err)
	}
	code, response := call(t, httpServer, "POST", "/api/projects/demo/resource-plan", testOwner, map[string]any{
		"cpu": 2, "memory_mb": 2048,
	})
	wantOK(t, code, response)
	if response["status"] != "no_match" {
		t.Fatalf("offline worker cannot be reserved: %v", response)
	}
	options, ok := response["candidates"].([]any)
	if !ok || len(options) != 1 {
		t.Fatalf("must return diagnostics for offline workers: %v", response)
	}
	candidate, _ := options[0].(map[string]any)
	if candidate["eligible"] != false || candidate["online"] != false {
		t.Fatalf("offline worker must be rejected: %v", candidate)
	}
	plan, err := server.callTool("resource_plan", map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512})
	if err != nil || plan.(map[string]any)["status"] != "no_match" {
		t.Fatalf("MCP plan disagrees: %v %v", plan, err)
	}
	rows, err := db.Workspaces("demo")
	if err != nil || len(rows) != 0 {
		t.Fatalf("advisory plan must never allocate a workspace: %v %v", rows, err)
	}
}

func TestProjectViewerCanPlanWithoutAllocating(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other-project", "Other"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	token := signedTestToken(t, key, issuer, "project-workspace", "viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	code, result := call(t, httpServer, "POST", "/api/projects/demo/resource-plan", token,
		map[string]any{"cpu": 1, "memory_mb": 1024})
	wantOK(t, code, result)
	if result["status"] != "no_match" {
		t.Fatalf("viewer read plan: %v", result)
	}
	code, _ = call(t, httpServer, "POST", "/api/workspaces", token,
		map[string]any{"project_id": "demo"})
	if code != 403 {
		t.Fatal("viewers must not allocate workers")
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/other-project/resource-plan", token,
		map[string]any{"cpu": 1})
	if code != 403 {
		t.Fatal("users must not plan resources for unauthorized projects")
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/demo/resource-plan", token,
		map[string]any{"project_id": "other-project"})
	if code == 200 {
		t.Fatal("request project ID cannot override authorization")
	}
}
