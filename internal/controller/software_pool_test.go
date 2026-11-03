package controller

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestImageAndSoftwarePool(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.RegisterPoolImage("debian-base", "debian:bookworm-slim", "Debian base", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterPoolSoftware("compilers", "debian-base", "C toolchain", []string{"gcc", "make"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterPoolSoftware("shell", "debian-base", "Shell utilities", []string{"curl", "make"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterPoolSoftware("unsafe", "debian-base", "", []string{"gcc;rm -rf /"}); err == nil {
		t.Fatal("package commands must not be accepted as names")
	}
	if _, err := db.RegisterPoolSoftware("compilers", "debian-base", "", []string{"gcc"}); err == nil {
		t.Fatal("catalog entries must be immutable")
	}
	selected, err := db.EnvironmentFromPool("demo", "build-env", "debian-base", []string{"compilers", "shell"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if asString(selected["status"]) != "defined" {
		t.Fatalf("environment must require a worker build: %v", selected)
	}
	row, err := db.queryOne("SELECT dockerfile FROM environments WHERE project=? AND name=?", "demo", "build-env")
	if err != nil {
		t.Fatal(err)
	}
	file := asString(row["dockerfile"])
	if !strings.HasPrefix(file, "FROM debian:bookworm-slim\nRUN apt-get update") {
		t.Fatalf("unexpected Dockerfile: %q", file)
	}
	if strings.Count(file, "make") != 1 {
		t.Fatalf("packages must be deduplicated: %q", file)
	}
	if _, err := db.EnvironmentFromPool("demo", "build-env", "debian-base", nil, 0); err == nil {
		t.Fatal("environment version guard must reject stale revision")
	}

	caps := caps()
	caps["images"] = []any{"ubuntu:24.04"}
	if _, err := db.RegisterNode("worker-one", caps); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "debian-base"}); err == nil {
		t.Fatal("unapproved image must not schedule on worker")
	}
	caps["images"] = []any{"debian:bookworm-slim"}
	if err := db.Heartbeat("worker-one", caps); err != nil {
		t.Fatal(err)
	}
	images, err := db.ImagePool()
	if err != nil || len(images) != 1 {
		t.Fatalf("image pool: %v %v", images, err)
	}
	nodes, ok := images[0]["available_nodes"].([]string)
	if !ok || len(nodes) != 1 || nodes[0] != "worker-one" {
		t.Fatalf("expected only approved worker: %v", images)
	}
	ws, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "debian-base"})
	if err != nil {
		t.Fatal(err)
	}
	if asString(ws["node"]) != "worker-one" || asString(ws["image_pool_id"]) != "debian-base" {
		t.Fatalf("incorrect pool allocation: %v", ws)
	}
}

func TestPoolRESTAndMCP(t *testing.T) {
	_, server, httpServer := fixture(t)
	project(t, httpServer)
	code, _ := call(t, httpServer, "POST", "/api/pool/images", "", map[string]any{"id": "debian-base", "image": "debian:bookworm-slim"})
	if code != 401 {
		t.Fatal("catalog mutations must require authorization")
	}
	code, value := call(t, httpServer, "POST", "/api/pool/images", testOwner, map[string]any{"id": "debian-base", "image": "debian:bookworm-slim"})
	wantOK(t, code, value)
	code, value = call(t, httpServer, "POST", "/api/pool/software", testOwner,
		map[string]any{"id": "compiler", "image_id": "debian-base", "packages": []string{"gcc", "make"}})
	wantOK(t, code, value)
	code, value = call(t, httpServer, "POST", "/api/projects/demo/environments/from-pool", testOwner,
		map[string]any{"name": "compiler-env", "image_id": "debian-base", "software_ids": []string{"compiler"}, "expected_version": 0})
	wantOK(t, code, value)
	if asString(value["name"]) != "compiler-env" {
		t.Fatalf("wrong environment: %v", value)
	}
	result, err := server.callTool("pool_list_software", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.([]map[string]any)) != 1 {
		t.Fatalf("unexpected MCP pool: %v", result)
	}
}

func TestMCPArtifactTransferIntegrity(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	data := []byte("small binary document\x00\x01")
	args := map[string]any{
		"project_id": "demo", "name": "document.bin",
		"sha256": checksum(data), "data_base64": base64.StdEncoding.EncodeToString(data),
	}
	value, err := server.callTool("artifact_send_base64", args)
	if err != nil {
		t.Fatal(err)
	}
	meta := value.(map[string]any)
	file, err := server.callTool("artifact_read_base64", map[string]any{"project_id": "demo", "artifact_id": meta["id"]})
	if err != nil {
		t.Fatal(err)
	}
	output, err := base64.StdEncoding.DecodeString(asString(file.(map[string]any)["data_base64"]))
	if err != nil || !bytes.Equal(output, data) {
		t.Fatalf("read mismatch: %v %v", output, err)
	}
	args["sha256"] = strings.Repeat("a", 64)
	if _, err := server.callTool("artifact_send_base64", args); err == nil {
		t.Fatal("mismatched checksum must be rejected")
	}
	args["sha256"] = checksum(data)
	args["data_base64"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), maxMCPArtifactBytes+1))
	if _, err := server.callTool("artifact_send_base64", args); err == nil {
		t.Fatal("oversize MCP transfer must fail")
	}
	if _, err := db.ReadMCPArtifact("unknown", asString(meta["id"])); err == nil {
		t.Fatal("cross-project download must fail")
	}
}
