package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const testOwner = "owner-secret-0123456789abcdef0123456789abcdef0123456789abcdef"

func fixture(t *testing.T) (*Database, *Server, *httptest.Server) {
	t.Helper()
	db, e := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	server, e := NewServer(db, testOwner)
	if e != nil {
		t.Fatal(e)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() { httpServer.Close(); db.Close() })
	return db, server, httpServer
}
func call(t *testing.T, ts *httptest.Server, method, path, token string, input any) (int, map[string]any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		payload, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		body = bytes.NewReader(payload)
	}
	req, e := http.NewRequest(method, ts.URL+path, body)
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, e := ts.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	data, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		t.Fatalf("%s %s response %d: %s", method, path, res.StatusCode, data)
	}
	return res.StatusCode, result
}
func wantOK(t *testing.T, code int, result map[string]any) {
	t.Helper()
	if code != 200 {
		t.Fatalf("unexpected HTTP %d: %#v", code, result)
	}
}
func project(t *testing.T, srv *httptest.Server) {
	t.Helper()
	c, r := call(t, srv, "POST", "/api/projects", testOwner, map[string]any{"id": "demo", "title": "Research"})
	wantOK(t, c, r)
}
func caps() map[string]any {
	return map[string]any{"cpu": 4, "memory_mb": 4096, "max_jobs": 2, "docker": true, "images": []any{"debian:bookworm-slim"}, "devices": []any{map[string]any{"id": "npu-one", "kind": "npu"}, map[string]any{"id": "usb-one", "kind": "usb"}}}
}
func pairWorker(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	c, r := call(t, srv, "POST", "/api/pairing", testOwner, nil)
	wantOK(t, c, r)
	c, r = call(t, srv, "POST", "/api/pair", "", map[string]any{"name": "worker-one", "pairing_code": r["pairing_code"], "capabilities": caps()})
	wantOK(t, c, r)
	return asString(r["worker_token"])
}
func claimOp(t *testing.T, srv *httptest.Server, token string) map[string]any {
	t.Helper()
	c, r := call(t, srv, "POST", "/api/worker/worker-one/operation/claim", token, nil)
	wantOK(t, c, r)
	op, _ := r["operation"].(map[string]any)
	if op == nil {
		t.Fatal("missing operation claim")
	}
	return op
}
func finishOp(t *testing.T, srv *httptest.Server, token string, op map[string]any, output map[string]any) {
	t.Helper()
	c, r := call(t, srv, "POST", "/api/worker/worker-one/operation/finish", token, map[string]any{"operation_id": op["id"], "lease_token": op["lease_token"], "success": true, "result": output})
	wantOK(t, c, r)
	if r["status"] != "succeeded" {
		t.Fatalf("unexpected completion: %v", r)
	}
}
func TestMCPHandshakeAndKnowledge(t *testing.T) {
	db, _, srv := fixture(t)
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}}
	c, r := call(t, srv, "POST", "/mcp/", "", req)
	if c != 401 {
		t.Fatalf("unauthenticated MCP accepted: %d %v", c, r)
	}
	c, r = call(t, srv, "POST", "/mcp/", testOwner, req)
	wantOK(t, c, r)
	if gitObj(gitObj(r["result"])["serverInfo"])["name"] != "workspace-mcp" {
		t.Fatal(r)
	}
	c, r = call(t, srv, "POST", "/mcp/", testOwner, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	wantOK(t, c, r)
	entries := gitObj(r["result"])["tools"].([]any)
	if len(entries) != len(tools) {
		t.Fatalf("expected %d tools got %d", len(tools), len(entries))
	}
	invoke := func(name string, args map[string]any) map[string]any {
		t.Helper()
		code, resp := call(t, srv, "POST", "/mcp/", testOwner, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		wantOK(t, code, resp)
		result := gitObj(resp["result"])
		if result["isError"] == true {
			t.Fatalf("tool %s failed: %v", name, result)
		}
		return gitObj(result["structuredContent"])
	}
	invoke("create_project", map[string]any{"project_id": "demo", "title": "Demo"})
	code, rejected := call(t, srv, "POST", "/mcp/", testOwner, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "write_project_note", "arguments": map[string]any{
			"project_id": "demo", "name": "plan", "body": "unconfigured local data", "expected_version": 0,
		}},
	})
	wantOK(t, code, rejected)
	if !asBool(gitObj(rejected["result"])["isError"]) {
		t.Fatal("MCP accepted a local knowledge write without Git storage")
	}
	if _, err := db.ReadNote("demo", "plan"); err == nil {
		t.Fatal("rejected knowledge write left persisted state")
	}
	// Historical SQLite notes remain readable for explicit migration.
	if _, err := db.WriteNote("demo", "plan", "Go-native persistent context", 0); err != nil {
		t.Fatal(err)
	}
	got := invoke("read_project_note", map[string]any{"project_id": "demo", "name": "plan"})
	if got["body"] != "Go-native persistent context" || asInt(got["version"]) != 1 {
		t.Fatalf("note mismatch: %v", got)
	}
}
func TestWorkspaceLeasesAndArtifacts(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	workerToken := pairWorker(t, srv)
	request := map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512, "hardware": map[string]any{"npu": 1}}
	c, result := call(t, srv, "POST", "/api/workspaces", testOwner, request)
	wantOK(t, c, result)
	ws := asString(result["id"])
	if ws == "" {
		t.Fatal("missing workspace ID")
	}
	c, second := call(t, srv, "POST", "/api/workspaces", testOwner, map[string]any{"project_id": "demo", "hardware": map[string]any{"npu": 1}})
	if c == 200 {
		t.Fatalf("exclusivity failure: %v", second)
	}
	start := claimOp(t, srv, workerToken)
	if start["action"] != "start" {
		t.Fatal(start)
	}
	finishOp(t, srv, workerToken, start, map[string]any{"status": "ready"})
	c, r := call(t, srv, "POST", "/api/projects/demo/workspaces/"+ws+"/operations", testOwner, map[string]any{"action": "snapshot", "payload": map[string]any{}})
	wantOK(t, c, r)
	op := claimOp(t, srv, workerToken)
	data := []byte("safely streamed workspace backup")
	h := sha256.Sum256(data)
	req, _ := http.NewRequest("PUT", srv.URL+"/api/worker/worker-one/workspaces/"+ws+"/operations/"+asString(op["id"])+"/artifact", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+workerToken)
	req.Header.Set("X-Operation-Token", asString(op["lease_token"]))
	req.Header.Set("X-Content-SHA256", hex.EncodeToString(h[:]))
	req.Header.Set("X-Artifact-Name", "snapshot.tar.gz")
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var uploaded map[string]any
	if e := json.NewDecoder(res.Body).Decode(&uploaded); e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != 200 {
		t.Fatalf("upload HTTP %d: %v", res.StatusCode, uploaded)
	}
	finishOp(t, srv, workerToken, op, map[string]any{"artifact_id": uploaded["id"]})
	rows, e := db.SnapshotList("demo", ws)
	if e != nil || len(rows) != 1 {
		t.Fatalf("snapshots: %v %v", rows, e)
	}
	artifact, e := db.ArtifactInfo("demo", asString(uploaded["id"]))
	if e != nil {
		t.Fatal(e)
	}
	path, e := db.VerifyArtifact(artifact)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(actual, data) {
		t.Fatalf("artifact mismatch: %v", e)
	}
	c, stop := call(t, srv, "POST", "/api/projects/demo/workspaces/"+ws+"/stop", testOwner, nil)
	wantOK(t, c, stop)
	finishOp(t, srv, workerToken, claimOp(t, srv, workerToken), map[string]any{"removed": true})
	c, again := call(t, srv, "POST", "/api/workspaces", testOwner, request)
	wantOK(t, c, again)
	if asString(again["id"]) == ws {
		t.Fatal("workspace ID reused")
	}
}
func TestUSBSelectionRequiresConsent(t *testing.T) {
	_, _, srv := fixture(t)
	project(t, srv)
	pairWorker(t, srv)
	c, r := call(t, srv, "POST", "/api/workspaces", testOwner, map[string]any{"project_id": "demo", "hardware": map[string]any{"usb": 1}})
	if c == 200 {
		t.Fatal(r)
	}
	c, r = call(t, srv, "POST", "/api/workspaces", testOwner, map[string]any{"project_id": "demo", "usb_passthrough": true, "usb_node": "worker-one", "usb_device_id": "wrong"})
	if c == 200 {
		t.Fatal(r)
	}
	c, r = call(t, srv, "POST", "/api/workspaces", testOwner, map[string]any{"project_id": "demo", "usb_passthrough": true, "usb_node": "worker-one", "usb_device_id": "usb-one"})
	wantOK(t, c, r)
}
func TestProjectAuthorizationAndDatabaseCompatibility(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	if e := db.exec("INSERT INTO notes VALUES(?,?,?,?,?)", "demo", "legacy", "existing SQLite note", 3, stamp()); e != nil {
		t.Fatal(e)
	}
	c, r := call(t, srv, "GET", "/api/projects/demo/notes/legacy", testOwner, nil)
	wantOK(t, c, r)
	if r["body"] != "existing SQLite note" || asInt(r["version"]) != 3 {
		t.Fatal(r)
	}
	c, r = call(t, srv, "POST", "/api/projects/demo/notes/legacy", testOwner, map[string]any{"body": "overwriting stale", "expected_version": 2})
	if c != 409 {
		t.Fatalf("stale notes allowed: %d %v", c, r)
	}
	c, r = call(t, srv, "POST", "/api/projects/demo/notes/legacy", testOwner, map[string]any{"body": "correct revision", "expected_version": 3})
	if c != http.StatusConflict {
		t.Fatalf("local knowledge overwrite accepted: %d %v", c, r)
	}
	c, r = call(t, srv, "GET", "/api/projects/demo/notes/legacy", testOwner, nil)
	wantOK(t, c, r)
	if asString(r["body"]) != "existing SQLite note" || asInt(r["version"]) != 3 {
		t.Fatalf("historic note changed: %v", r)
	}
	c, r = call(t, srv, "GET", "/api/projects/demo/notes/legacy", "wrong-token", nil)
	if c != 401 {
		t.Fatal(c, r)
	}
}

func TestRejectsMalformedHardwareQuantities(t *testing.T) {
	_, _, srv := fixture(t)
	project(t, srv)
	pairWorker(t, srv)
	invalid := []map[string]any{
		{"npu": true}, {"npu": "1"}, {"npu": 0.5}, {"gpu": -1},
	}
	for _, hardware := range invalid {
		code, reply := call(t, srv, "POST", "/api/workspaces", testOwner,
			map[string]any{"project_id": "demo", "hardware": hardware})
		if code == 200 {
			t.Fatalf("accepted malformed hardware request %v: %v", hardware, reply)
		}
	}
}
