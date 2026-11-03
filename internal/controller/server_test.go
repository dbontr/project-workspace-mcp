package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func controllerForTest(t *testing.T) (*Server, *Database, *httptest.Server) {
	t.Helper()
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+strings.Repeat("a", 32) {
			fmt.Fprintf(w, "proxied:%s", r.URL.Path)
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	service, err := NewServer(db, strings.Repeat("a", 32), target)
	if err != nil {
		t.Fatal(err)
	}
	return service, db, upstream
}

func request(t *testing.T, h http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var content *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		content = bytes.NewReader(data)
	} else {
		content = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, content)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	result := map[string]any{}
	_ = json.Unmarshal(out.Body.Bytes(), &result)
	return out.Code, result
}

func TestKnowledgeIsNativeGoAndRevisionGuarded(t *testing.T) {
	h, db, _ := controllerForTest(t)
	admin := strings.Repeat("a", 32)
	if status, _ := request(t, h, "POST", "/api/projects", "", map[string]any{"id": "demo", "title": "Demo"}); status != 401 {
		t.Fatalf("missing auth: %d", status)
	}
	if status, _ := request(t, h, "POST", "/api/projects", admin, map[string]any{"id": "demo", "title": "Demo"}); status != 200 {
		t.Fatalf("create project: %d", status)
	}
	if status, _ := request(t, h, "POST", "/api/projects/demo/notes/design", admin, map[string]any{"body": "A knowledge graph", "expected_version": 0}); status != 200 {
		t.Fatalf("create note: %d", status)
	}
	if status, _ := request(t, h, "POST", "/api/projects/demo/notes/design", admin, map[string]any{"body": "Lost edit", "expected_version": 0}); status != 409 {
		t.Fatalf("accepted stale revision: %d", status)
	}
	if status, _ := request(t, h, "POST", "/api/projects/demo/notes/design", admin, map[string]any{"body": "Missing version"}); status != 400 {
		t.Fatalf("accepted missing version: %d", status)
	}
	if status, note := request(t, h, "GET", "/api/projects/demo/notes/design", admin, nil); status != 200 || note["body"] != "A knowledge graph" || note["version"].(float64) != 1 {
		t.Fatalf("read: %d %v", status, note)
	}
	result, err := db.SearchNotes("demo", "knowledge")
	if err != nil || len(result) != 1 || result[0]["name"] != "design" {
		t.Fatalf("search: %v %v", result, err)
	}
	if _, err := db.WriteNote("other", "design", "cross-project", 0); err == nil {
		t.Fatal("cross-project note accepted")
	}
	if status, _ := request(t, h, "POST", "/api/projects", admin, map[string]any{"id": "Bad!", "title": "No"}); status != 400 {
		t.Fatalf("bad project accepted %d", status)
	}
	if status, _ := request(t, h, "GET", "/api/projects/demo/notes/../design", admin, nil); status == 200 {
		t.Fatal("invalid note traversal accepted")
	}
}

func TestControllerForwardsUnmigratedRoutes(t *testing.T) {
	h, _, _ := controllerForTest(t)
	admin := strings.Repeat("a", 32)
	req := httptest.NewRequest("GET", "/mcp/", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 200 || out.Body.String() != "proxied:/mcp/" {
		t.Fatalf("MCP proxy: %d %s", out.Code, out.Body.String())
	}
	if status, _ := request(t, h, "GET", "/api/projects", admin, nil); status != 200 {
		t.Fatalf("native route failed %d", status)
	}
	if _, err := NewServer(h.DB, admin, &url.URL{Scheme: "https", Host: "other.example"}); err == nil {
		t.Fatal("accepted untrusted upstream")
	}
}

func TestRevisionWritesAreSerialized(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateProject("demo", "Demo"); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 12)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(i int) { defer group.Done(); _, err := db.WriteNote("demo", "note", fmt.Sprint(i), 0); errs <- err }(i)
	}
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one writer to win, got %d", successes)
	}
}
