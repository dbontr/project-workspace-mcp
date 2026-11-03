package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestResumableLargeLibraryObjectsSurviveControllerRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "controller.db")
	db, e := OpenDatabase(file)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.CreateLibrary("lean-library", "Lean mathematics", "", "lean"); e != nil {
		t.Fatal(e)
	}
	data := bytes.Repeat([]byte("lean-cache-binary"), 1_350_000)
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	session, e := db.StartLibraryUpload("lean-library", "cache-2026.tar.zst", "build-cache", sha, int64(len(data)), 100<<30)
	if e != nil {
		t.Fatal(e)
	}
	id := asString(session["upload_id"])
	if session["chunk_size"] != libraryChunkSize {
		t.Fatalf("unexpected chunk limit: %v", session)
	}
	first, e := db.AppendLibraryChunk("lean-library", id, 0, bytes.NewReader(data[:int(libraryChunkSize)]))
	if e != nil || int64(asFloat(first["received_bytes"])) != libraryChunkSize {
		t.Fatalf("first chunk failed %v %v", first, e)
	}
	_, e = db.AppendLibraryChunk("lean-library", id, 0, bytes.NewReader([]byte("replay")))
	if e == nil {
		t.Fatal("replayed or conflicting byte offset accepted")
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = OpenDatabase(file)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	status, e := db.uploadRow("lean-library", id)
	if e != nil || int64(asFloat(status["received"])) != libraryChunkSize {
		t.Fatalf("upload did not survive restart: %v %v", status, e)
	}
	offset := libraryChunkSize
	for offset < int64(len(data)) {
		end := offset + libraryChunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, e = db.AppendLibraryChunk("lean-library", id, offset, bytes.NewReader(data[int(offset):int(end)]))
		if e != nil {
			t.Fatal(e)
		}
		offset = end
	}
	stored, e := db.CompleteLibraryUpload("lean-library", id)
	if e != nil {
		t.Fatal(e)
	}
	if asString(stored["sha256"]) != sha {
		t.Fatal(stored)
	}
	f, _, e := db.OpenLibraryObject("lean-library", "cache-2026.tar.zst")
	if e != nil {
		t.Fatal(e)
	}
	buf, e := io.ReadAll(f)
	f.Close()
	if e != nil || !bytes.Equal(buf, data) {
		t.Fatal("resumed artifact contents changed")
	}
	if _, e = db.uploadRow("lean-library", id); e == nil {
		t.Fatal("completed upload session was not cleared")
	}
	// A 6 GiB upload is supported without preallocating memory or staging space.
	big, e := db.StartLibraryUpload("lean-library", "large-proof-index.tar.zst", "build-cache", sha, 6<<30, 100<<30)
	if e != nil {
		t.Fatalf("large logical upload rejected: %v", e)
	}
	if e := db.CancelLibraryUpload("lean-library", asString(big["upload_id"])); e != nil {
		t.Fatal(e)
	}
}

func TestChunkedUploadRESTAndChecksumFailures(t *testing.T) {
	db, _, srv := fixture(t)
	if _, e := db.CreateLibrary("research-library", "Research", "", "research"); e != nil {
		t.Fatal(e)
	}
	data := bytes.Repeat([]byte("research"), 2048)
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	code, start := call(t, srv, "POST", "/api/libraries/research-library/uploads", testOwner, map[string]any{"name": "paper.pdf", "kind": "paper", "sha256": sha, "size": len(data)})
	wantOK(t, code, start)
	id := asString(start["upload_id"])
	sendChunk := func(offset int64, contents []byte) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("PUT", srv.URL+"/api/libraries/research-library/uploads/"+id, bytes.NewReader(contents))
		req.Header.Set("Authorization", "Bearer "+testOwner)
		req.Header.Set("X-Upload-Offset", strconv.FormatInt(offset, 10))
		res, e := srv.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var m map[string]any
		if e := json.NewDecoder(res.Body).Decode(&m); e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, m
	}
	if code, _ := sendChunk(1, data[:30]); code == 200 {
		t.Fatal("wrong offset accepted")
	}
	if code, _ := sendChunk(0, data[:30]); code != 200 {
		t.Fatal("first chunk rejected")
	}
	if code, _ := sendChunk(30, data[30:]); code != 200 {
		t.Fatal("final chunk rejected")
	}
	c, result := call(t, srv, "POST", "/api/libraries/research-library/uploads/"+id+"/complete", testOwner, nil)
	wantOK(t, c, result)
	if result["sha256"] != sha {
		t.Fatal(result)
	}
	f, _, e := db.OpenLibraryObject("research-library", "paper.pdf")
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	// The same name cannot be silently replaced by a second upload.
	c, _ = call(t, srv, "POST", "/api/libraries/research-library/uploads", testOwner, map[string]any{"name": "paper.pdf", "kind": "paper", "sha256": sha, "size": len(data)})
	if c == 200 {
		t.Fatal("immutable library object name reused")
	}

	wrong := sha256.Sum256([]byte("different"))
	c, failing := call(t, srv, "POST", "/api/libraries/research-library/uploads", testOwner, map[string]any{"name": "wrong.pdf", "kind": "paper", "sha256": hex.EncodeToString(wrong[:]), "size": len(data)})
	wantOK(t, c, failing)
	wrongID := asString(failing["upload_id"])
	req, _ := http.NewRequest("PUT", srv.URL+"/api/libraries/research-library/uploads/"+wrongID, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+testOwner)
	req.Header.Set("X-Upload-Offset", "0")
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("expected upload completion before digest check")
	}
	c, _ = call(t, srv, "POST", "/api/libraries/research-library/uploads/"+wrongID+"/complete", testOwner, nil)
	if c == 200 {
		t.Fatal("incorrect SHA-256 was published")
	}
	if _, _, e := db.OpenLibraryObject("research-library", "wrong.pdf"); e == nil {
		t.Fatal("failed upload visible as an object")
	}
	if _, e := db.uploadRow("research-library", wrongID); e != nil {
		t.Fatal("failed upload lost retry/cancel state")
	}
	if e := db.CancelLibraryUpload("research-library", wrongID); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(db.uploadPath(wrongID)); !os.IsNotExist(e) {
		t.Fatalf("staging file leaked: %v", e)
	}
}

// The TLS ingress can impose a per-request body limit; a bounded chunk route
// is deliberately smaller than typical HTTP proxy request caps.
func TestChunkSizeBound(t *testing.T) {
	_, _, srv := fixture(t)
	req, _ := http.NewRequest("POST", srv.URL+"/api/libraries", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer "+testOwner)
	// The route retains authentication even for malformed upload metadata.
	response, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("malformed library request accepted")
	}
	if libraryChunkSize >= 100<<20 {
		t.Fatal("upload chunks exceed intended proxy-safe bound")
	}
}
