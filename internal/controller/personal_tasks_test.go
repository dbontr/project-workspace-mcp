package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonalTasksIndependentAndImmutable(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	task, err := db.CreatePersonalTask("controller-owner", "Circuit simulation", "LTspice notes", []string{"circuits"})
	if err != nil {
		t.Fatal(err)
	}
	id := asString(task["id"])
	if len(id) != 32 {
		t.Fatalf("unexpected personal task id: %v", task)
	}
	if _, err := db.CreatePersonalTask("controller-owner", "Duplicate tags", "", []string{"lab", "lab"}); err == nil {
		t.Fatal("duplicate tags must fail")
	}
	first, err := db.SavePersonalTaskText("controller-owner", id, "notes.txt", "version one", 0)
	if err != nil || asInt(first["revision"]) != 1 {
		t.Fatalf("first revision: %v %v", first, err)
	}
	second, err := db.SavePersonalTaskText("controller-owner", id, "notes.txt", "version two", 1)
	if err != nil || asInt(second["revision"]) != 2 {
		t.Fatalf("second revision: %v %v", second, err)
	}
	if _, err := db.SavePersonalTaskText("controller-owner", id, "notes.txt", "overwritten", 1); err == nil {
		t.Fatal("stale file revision must fail")
	}
	if _, err := db.SavePersonalTaskText("oidc:https://issuer.example#other", id, "notes.txt", "foreign", 2); err == nil {
		t.Fatal("different task owner must be denied")
	}
	earlier, err := db.PersonalTaskFileBase64("controller-owner", id, "notes.txt", 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(asString(earlier["data_base64"]))
	if err != nil || string(data) != "version one" {
		t.Fatalf("old file was not preserved: %q %v", data, err)
	}
	file, record, err := db.OpenPersonalTaskFile("controller-owner", id, "notes.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(raw) != "version two" || asInt(record["revision"]) != 2 {
		t.Fatalf("latest revision incorrect: %q %v", raw, err)
	}
	files, err := db.PersonalTaskFiles("controller-owner", id)
	if err != nil || len(files) != 2 {
		t.Fatalf("revision history: %v %v", files, err)
	}
	info, err := db.UpdatePersonalTask("controller-owner", id, "Circuit simulation", "Archived task", "archived", []string{"circuits"}, 1)
	if err != nil || asInt(info["version"]) != 2 || info["status"] != "archived" {
		t.Fatalf("archive task: %v %v", info, err)
	}
	if _, err := db.SavePersonalTaskText("controller-owner", id, "notes.txt", "new", 2); err == nil {
		t.Fatal("archived tasks may not be modified")
	}
	if _, err := db.UpdatePersonalTask("controller-owner", id, "Circuit simulation", "", "active", nil, 1); err == nil {
		t.Fatal("stale task metadata must fail")
	}
	if _, err := db.UpdatePersonalTask("controller-owner", id, "Circuit simulation", "Active", "active", []string{"circuits"}, 2); err != nil {
		t.Fatal(err)
	}
	searched, err := db.PersonalTasks("controller-owner", "simulation")
	if err != nil || len(searched) != 1 {
		t.Fatalf("personal task search: %v %v", searched, err)
	}
	other, err := db.PersonalTasks("oidc:https://issuer.example#other", "")
	if err != nil || len(other) != 0 {
		t.Fatalf("task isolation failure: %v %v", other, err)
	}
}

func TestPersonalTaskStreamPromotionAndBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source", "state.db")
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Artifact analysis", "Portable workspace files", []string{"research"})
	if err != nil {
		t.Fatal(err)
	}
	id := asString(task["id"])
	data := bytes.Repeat([]byte("binary\x00"), 125000)
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	stored, err := db.SavePersonalTaskStream("controller-owner", id, "data.bin", digest, bytes.NewReader(data), 10<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(stored["size"]) != len(data) {
		t.Fatalf("truncated upload: %v", stored)
	}
	if _, err := db.SavePersonalTaskStream("controller-owner", id, "invalid.bin", strings.Repeat("a", 64), bytes.NewReader(data), 10<<20, 0); err == nil {
		t.Fatal("checksum mismatch must fail")
	}
	if _, err := db.SavePersonalTaskStream("controller-owner", id, "short.bin", digest, bytes.NewReader(data), 1024, 0); err == nil {
		t.Fatal("upload size limit must be enforced")
	}
	if _, err := db.PersonalTaskFileBase64("controller-owner", id, "data.bin", 0); err == nil {
		t.Fatal("large binary files must use streaming download")
	}
	promoted, err := db.PromotePersonalTaskFile("controller-owner", id, "data.bin", 1, "research")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := db.ArtifactInfo("research", asString(promoted["id"]))
	if err != nil || meta["sha256"] != digest {
		t.Fatalf("promoted artifact integrity: %v %v", meta, err)
	}
	if _, err := db.ArtifactInfo("research", asString(stored["artifact_id"])); err == nil {
		t.Fatal("personal task file must not become a project artifact before explicit promotion")
	}
	backup := filepath.Join(root, "backup")
	if err := db.BackupTo(backup); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored", "state.db")
	if err := RestoreBackup(backup, target); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original, err := db.PersonalTaskFile("controller-owner", id, "data.bin", 1)
	if err != nil || original["sha256"] != digest {
		t.Fatalf("personal task file lost after restore: %v %v", original, err)
	}
	file, _, err := db.OpenPersonalTaskFile("controller-owner", id, "data.bin", 1)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(restored, data) {
		t.Fatalf("backup omitted task bytes: %v", err)
	}
}

func TestPersonalTaskRESTAndIdentityIsolation(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other-project", "Other"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	alice := signedTestToken(t, key, issuer, "project-workspace", "alice", time.Now().Add(time.Hour).Unix())
	bob := signedTestToken(t, key, issuer, "project-workspace", "bob", time.Now().Add(time.Hour).Unix())
	code, task := call(t, httpServer, http.MethodPost, "/api/tasks", alice,
		map[string]any{"title": "Lab notebook", "description": "Circuit measurements", "tags": []string{"laboratory"}})
	wantOK(t, code, task)
	id := asString(task["id"])
	code, _ = call(t, httpServer, http.MethodGet, "/api/tasks/"+id, bob, nil)
	if code == http.StatusOK {
		t.Fatal("other user cannot read task")
	}
	code, _ = call(t, httpServer, http.MethodPatch, "/api/tasks/"+id, bob,
		map[string]any{"title": "Changed", "status": "active", "expected_version": 1})
	if code == http.StatusOK {
		t.Fatal("other user cannot edit task")
	}
	// Verify an authenticated binary stream without any project attachment.
	data := []byte{0, 1, 2, 3, 255}
	digest := checksum(data)
	req, err := http.NewRequest(http.MethodPut, httpServer.URL+"/api/tasks/"+id+"/files/sample.bin", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+alice)
	req.Header.Set("X-Content-SHA256", digest)
	req.Header.Set("X-Expected-Revision", "0")
	res, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("task stream failed: %d %s", res.StatusCode, raw)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["sha256"] != digest {
		t.Fatalf("stream digest mismatch: %v", decoded)
	}
	req, err = http.NewRequest(http.MethodGet, httpServer.URL+"/api/tasks/"+id+"/files/sample.bin/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+alice)
	res, err = httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	download, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(download, data) || res.Header.Get("X-Content-SHA256") != digest {
		t.Fatalf("download failed integrity: %d %q", res.StatusCode, download)
	}
	req.Header.Set("Authorization", "Bearer "+bob)
	res, err = httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("other owner must not download task")
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/files/sample.bin/promote", alice,
		map[string]any{"project_id": "demo"})
	if code != http.StatusForbidden {
		t.Fatal("personal task owner cannot bypass project editor grants")
	}
	owner := "oidc:" + issuer + "#alice"
	if _, err := db.SetProjectGrant("demo", owner, "editor", ""); err != nil {
		t.Fatal(err)
	}
	code, promoted := call(t, httpServer, http.MethodPost, "/api/tasks/"+id+"/files/sample.bin/promote", alice,
		map[string]any{"project_id": "demo", "revision": 1})
	wantOK(t, code, promoted)
	if promoted["project"] != "demo" {
		t.Fatalf("promoted file missing project: %v", promoted)
	}
	if _, err := db.ArtifactInfo("demo", asString(promoted["id"])); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalTaskMCPIdentityAndVersionChecks(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	key, issuer := testOIDCIssuer(t, server)
	alice := signedTestToken(t, key, issuer, "project-workspace", "alice", time.Now().Add(time.Hour).Unix())
	bob := signedTestToken(t, key, issuer, "project-workspace", "bob", time.Now().Add(time.Hour).Unix())
	callMCP := func(token, name string, args map[string]any) map[string]any {
		t.Helper()
		code, response := call(t, httpServer, http.MethodPost, "/mcp/", token, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		wantOK(t, code, response)
		out, _ := response["result"].(map[string]any)
		return out
	}
	first := callMCP(alice, "task_create", map[string]any{"title": "Private dataset", "tags": []string{"experimental"}})
	if asBool(first["isError"]) {
		t.Fatalf("task creation via MCP failed: %v", first)
	}
	structured, _ := first["structuredContent"].(map[string]any)
	id := asString(structured["id"])
	if id == "" {
		t.Fatalf("task ID absent: %v", first)
	}
	foreign := callMCP(bob, "task_details", map[string]any{"task_id": id})
	if !asBool(foreign["isError"]) {
		t.Fatal("other principal could read private task")
	}
	created := callMCP(alice, "task_send_base64", map[string]any{
		"task_id": id, "name": "proof.bin", "sha256": checksum([]byte("proof")),
		"data_base64":       base64.StdEncoding.EncodeToString([]byte("proof")),
		"expected_revision": 0,
	})
	if asBool(created["isError"]) {
		t.Fatalf("binary transfer failed: %v", created)
	}
	stale := callMCP(alice, "task_save_text", map[string]any{
		"task_id": id, "name": "proof.bin", "body": "conflicting",
		"expected_revision": 0,
	})
	if !asBool(stale["isError"]) {
		t.Fatal("MCP task write must reject stale revisions")
	}
	encoded := callMCP(alice, "task_read_base64", map[string]any{"task_id": id, "name": "proof.bin"})
	if asBool(encoded["isError"]) {
		t.Fatalf("read back failed: %v", encoded)
	}
	payload, _ := encoded["structuredContent"].(map[string]any)
	if payload["sha256"] != checksum([]byte("proof")) {
		t.Fatal("file checksum unavailable")
	}
	if _, err := db.PersonalTaskInfo("oidc:"+issuer+"#alice", id); err != nil {
		t.Fatal(err)
	}
}
