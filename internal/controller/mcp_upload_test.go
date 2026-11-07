package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func uploadTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestResumableMCPArtifactLargeBinaryAcrossDatabaseRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "state.db")
	db, err := OpenDatabase(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("uploads", "Upload integration"); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, (2<<20)+17)
	for i := range data {
		data[i] = byte((i*37 + 19) % 251)
	}
	digest := uploadTestDigest(data)
	owner := "controller-owner"
	session, err := db.BeginMCPUpload(owner, "uploads", "", "binary-test.bin", digest, int64(len(data)), 0, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(session["upload_id"])
	first := base64.StdEncoding.EncodeToString(data[:200000])
	progress, err := db.AppendMCPUpload(owner, id, "uploads", "", 0, first)
	if err != nil || asInt(progress["received_bytes"]) != 200000 {
		t.Fatalf("initial chunk: %v %v", progress, err)
	}
	if _, err := db.AppendMCPUpload(owner, id, "uploads", "", 0, first); err == nil {
		t.Fatal("stale upload offset accepted")
	}
	if _, err := db.AppendMCPUpload("another-principal", id, "uploads", "", 200000, first); err == nil {
		t.Fatal("cross-owner upload accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	status, err := db.MCPUploadStatus(owner, id, "uploads", "")
	if err != nil || asInt(status["received_bytes"]) != 200000 {
		t.Fatalf("upload not resumed after controller restart: %v %v", status, err)
	}
	for offset := 200000; offset < len(data); {
		end := offset + 210000
		if end > len(data) {
			end = len(data)
		}
		next, err := db.AppendMCPUpload(owner, id, "uploads", "", int64(offset),
			base64.StdEncoding.EncodeToString(data[offset:end]))
		if err != nil || asInt(next["received_bytes"]) != end {
			t.Fatalf("append at %d: %v %v", offset, next, err)
		}
		offset = end
	}
	result, err := db.FinishMCPUpload(owner, id, "uploads", "", 10<<20)
	if err != nil || result["sha256"] != digest {
		t.Fatalf("completion failed: %v %v", result, err)
	}
	again, err := db.FinishMCPUpload(owner, id, "uploads", "", 10<<20)
	if err != nil || again["id"] != result["id"] {
		t.Fatalf("duplicate finish created another artifact: %v %v", again, err)
	}
	artifact, err := db.ArtifactInfo("uploads", asString(result["id"]))
	if err != nil {
		t.Fatal(err)
	}
	path, err := db.VerifyArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(readback, data) {
		t.Fatalf("large binary artifact readback failed: %v", err)
	}
	if _, err := db.MCPUploadStatus(owner, id, "another-project", ""); err == nil {
		t.Fatal("cross-project progress inspected")
	}
}

func TestResumableMCPTaskRevisionsDigestMismatchAndCancellation(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := "controller-owner"
	task, err := db.CreatePersonalTask(owner, "Upload test task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID := asString(task["id"])
	content := []byte("source bytes must be kept immutable\x00\xff")
	digest := uploadTestDigest(content)
	begin := func(digest string, expected int) string {
		t.Helper()
		session, err := db.BeginMCPUpload(owner, "", taskID, "attachment.bin", digest, int64(len(content)), expected, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return asString(session["upload_id"])
	}
	id := begin(strings.Repeat("0", 64), 0)
	if _, err := db.AppendMCPUpload(owner, id, "", taskID, 0, base64.StdEncoding.EncodeToString(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishMCPUpload(owner, id, "", taskID, 1<<20); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, err := db.CancelMCPUpload(owner, id, "", taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MCPUploadStatus(owner, id, "", taskID); err == nil {
		t.Fatal("cancelled transfer still visible")
	}
	id = begin(digest, 0)
	if _, err := db.AppendMCPUpload(owner, id, "", taskID, 0, base64.StdEncoding.EncodeToString(content)); err != nil {
		t.Fatal(err)
	}
	result, err := db.FinishMCPUpload(owner, id, "", taskID, 1<<20)
	if err != nil || asInt(result["revision"]) != 1 {
		t.Fatalf("immutable task revision not saved: %v %v", result, err)
	}
	read, err := db.PersonalTaskFileBase64(owner, taskID, "attachment.bin", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(asString(read["data_base64"]))
	if err != nil || !bytes.Equal(raw, content) {
		t.Fatalf("task file readback mismatch: %v", err)
	}
	id = begin(digest, 0)
	if _, err := db.AppendMCPUpload(owner, id, "", taskID, 0, base64.StdEncoding.EncodeToString(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishMCPUpload(owner, id, "", taskID, 1<<20); err == nil {
		t.Fatal("stale task revision accepted")
	}
	if _, err := db.CancelMCPUpload(owner, id, "", taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginMCPUpload("other-person", "", taskID, "attachment.bin", digest, int64(len(content)), 1, 1<<20); err == nil {
		t.Fatal("foreign owner initiated a personal task upload")
	}
}

func TestResumableMCPChunkValidation(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := "controller-owner"
	if _, err := db.CreateProject("xfer", "Transfer"); err != nil {
		t.Fatal(err)
	}
	bytes := []byte{0, 1, 2, 4}
	session, err := db.BeginMCPUpload(owner, "xfer", "", "payload.bin", uploadTestDigest(bytes), int64(len(bytes)), 0, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(session["upload_id"])
	for _, bad := range []string{"invalid", base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3, 4})} {
		if _, err := db.AppendMCPUpload(owner, id, "xfer", "", 0, bad); err == nil {
			t.Fatalf("invalid chunk accepted: %q", bad)
		}
	}
	if _, err := db.FinishMCPUpload(owner, id, "xfer", "", 10<<20); err == nil {
		t.Fatal("incomplete upload accepted")
	}
	if _, err := db.AppendMCPUpload(owner, id, "xfer", "", 0, base64.StdEncoding.EncodeToString(bytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendMCPUpload(owner, id, "xfer", "", int64(len(bytes)), base64.StdEncoding.EncodeToString(bytes)); err == nil {
		t.Fatal("upload beyond declared size accepted")
	}
	if _, err := db.BeginMCPUpload(owner, "", "", "bad.bin", uploadTestDigest(bytes), int64(len(bytes)), 0, 10<<20); err == nil {
		t.Fatal("upload without project/task scope accepted")
	}
	if _, err := db.BeginMCPUpload(owner, "xfer", "", "../escape", uploadTestDigest(bytes), int64(len(bytes)), 0, 10<<20); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := db.CancelMCPUpload(owner, id, "xfer", ""); err != nil {
		t.Fatal(err)
	}
}

func TestResumableMCPJSONRPCTransferAndProjectAuthorization(t *testing.T) {
	_, _, httpServer := fixture(t)
	project(t, httpServer)
	transfer := func(name string, arguments map[string]any) (map[string]any, bool) {
		t.Helper()
		status, response := call(t, httpServer, "POST", "/mcp/", testOwner, map[string]any{
			"jsonrpc": "2.0", "id": 31, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": arguments},
		})
		wantOK(t, status, response)
		result := gitObj(response["result"])
		return gitObj(result["structuredContent"]), result["isError"] == true
	}
	data := bytes.Repeat([]byte("verified-client-attachment"), 45000)
	digest := uploadTestDigest(data)
	start, failed := transfer("upload_begin", map[string]any{
		"project_id": "demo", "name": "source.bin",
		"sha256": digest, "size_bytes": len(data),
	})
	if failed {
		t.Fatalf("MCP begin failed: %v", start)
	}
	id := asString(start["upload_id"])
	received := 0
	for received < len(data) {
		end := received + 150000
		if end > len(data) {
			end = len(data)
		}
		status, failed := transfer("upload_chunk", map[string]any{
			"project_id": "demo", "upload_id": id, "offset": received,
			"data_base64": base64.StdEncoding.EncodeToString(data[received:end]),
		})
		if failed || asInt(status["received_bytes"]) != end {
			t.Fatalf("MCP chunk rejected at %d: %v failed=%v", received, status, failed)
		}
		received = end
	}
	progress, failed := transfer("upload_status", map[string]any{
		"project_id": "demo", "upload_id": id,
	})
	if failed || progress["state"] != "open" || asInt(progress["received_bytes"]) != len(data) {
		t.Fatalf("MCP status: %v failed=%v", progress, failed)
	}
	result, failed := transfer("upload_finish", map[string]any{
		"project_id": "demo", "upload_id": id,
	})
	if failed || result["sha256"] != digest {
		t.Fatalf("MCP finish: %v failed=%v", result, failed)
	}
	repeat, failed := transfer("upload_finish", map[string]any{
		"project_id": "demo", "upload_id": id,
	})
	if failed || repeat["id"] != result["id"] {
		t.Fatalf("MCP finish retry not idempotent: %v", repeat)
	}
}

func TestResumableUploadDeniedToNonEditor(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	key, issuer := testOIDCIssuer(t, server)
	token := signedTestToken(t, key, issuer, "project-workspace",
		"upload-viewer", time.Now().Add(time.Hour).Unix())
	principal := "oidc:" + issuer + "#upload-viewer"
	if _, err := db.SetProjectGrant("demo", principal, "viewer", ""); err != nil {
		t.Fatal(err)
	}
	data := []byte("unauthorized write")
	arguments := map[string]any{
		"project_id": "demo", "name": "denied.txt",
		"sha256": uploadTestDigest(data), "size_bytes": len(data),
	}
	status, response := call(t, httpServer, "POST", "/mcp/", token, map[string]any{
		"jsonrpc": "2.0", "id": 33, "method": "tools/call",
		"params": map[string]any{"name": "upload_begin", "arguments": arguments},
	})
	wantOK(t, status, response)
	if gitObj(response["result"])["isError"] != true {
		t.Fatalf("viewer uploaded project file: %v", response)
	}
}

func TestResumableMCPConcurrentFinishIsIdempotent(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateProject("uploads", "Concurrent upload"); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("ABCDEF012345"), 12000)
	owner := "controller-owner"
	session, err := db.BeginMCPUpload(owner, "uploads", "", "result.bin",
		uploadTestDigest(payload), int64(len(payload)), 0, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	id := asString(session["upload_id"])
	_, err = db.AppendMCPUpload(owner, id, "uploads", "", 0,
		base64.StdEncoding.EncodeToString(payload))
	if err != nil {
		t.Fatal(err)
	}
	type finished struct {
		id string
		err error
	}
	out := make(chan finished, 12)
	for i := 0; i < cap(out); i++ {
		go func() {
			result, err := db.FinishMCPUpload(owner, id, "uploads", "", 4<<20)
			out <- finished{id: asString(result["id"]), err: err}
		}()
	}
	var firstID string
	for i := 0; i < cap(out); i++ {
		got := <-out
		if got.err != nil {
			t.Fatalf("concurrent finalize: %v", got.err)
		}
		if firstID == "" {
			firstID = got.id
		} else if got.id != firstID {
			t.Fatalf("duplicate artifact: %s versus %s", firstID, got.id)
		}
	}
	files, err := db.Artifacts("uploads")
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one committed artifact, got %d: %v", len(files), err)
	}
	if _, err := db.CancelMCPUpload(owner, id, "uploads", ""); err == nil {
		t.Fatal("completed upload was cancelled")
	}
}
