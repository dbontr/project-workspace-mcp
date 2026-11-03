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
	"strings"
	"testing"
)

const testProjectID = "PVT_kwDOLibraryProject1234"
const testItemID = "PVTI_kwDOProjectItem5678"
const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGitHubProjectsNativePlanningAndMultiRepository(t *testing.T) {
	db, controller, srv := fixture(t)
	project(t, srv)
	_, e := db.CreateProject("other", "Independent research")
	if e != nil {
		t.Fatal(e)
	}
	calls := []string{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer project-token" {
			t.Errorf("unexpected GitHub request %s", r.URL)
			http.Error(w, "unauthorized", 401)
			return
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Errorf("invalid GraphQL: %v", e)
			return
		}
		calls = append(calls, request.Query)
		var payload map[string]any
		switch {
		case strings.Contains(request.Query, "ProjectV2SingleSelectField"):
			payload = map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": testProjectID,
				"url": "https://github.com/users/scholar/projects/5",
				"fields": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []any{map[string]any{"id": "PVTSSF_field123", "name": "Status",
						"options": []any{map[string]any{"id": "option12345", "name": "In Progress"}}}}}}}
			if request.Variables["after"] != "page-2" {
				t.Errorf("missing field pagination cursor: %v", request.Variables)
			}
		case strings.Contains(request.Query, "ProjectV2ItemFieldTextValue"):
			payload = map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": testProjectID, "title": "Research board", "url": "https://github.com/users/scholar/projects/5", "readme": "# Research index", "items": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": []any{map[string]any{"id": testItemID, "type": "ISSUE", "content": map[string]any{"title": "Formalize theorem"}}}}}}
			if request.Variables["after"] != "cursor" {
				t.Errorf("missing pagination cursor: %v", request.Variables)
			}
		case strings.Contains(request.Query, "addProjectV2DraftIssue"):
			payload = map[string]any{"addProjectV2DraftIssue": map[string]any{"projectItem": map[string]any{"id": testItemID}}}
		case strings.Contains(request.Query, "addProjectV2ItemById"):
			if request.Variables["content"] != "ISSUE_123456789" {
				t.Errorf("wrong content node: %v", request.Variables)
			}
			payload = map[string]any{"addProjectV2ItemById": map[string]any{"item": map[string]any{"id": testItemID}}}
		case strings.Contains(request.Query, "repository(owner:"):
			if request.Variables["owner"] != "elsewhere" || request.Variables["repo"] != "formalizations" {
				t.Errorf("wrong repository: %v", request.Variables)
			}
			payload = map[string]any{"repository": map[string]any{"issue": map[string]any{"id": "ISSUE_123456789"}}}
		case strings.Contains(request.Query, "updateProjectV2ItemFieldValue"):
			payload = map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]any{"id": testItemID}}}
		case strings.Contains(request.Query, "node(id:"):
			payload = map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": testProjectID, "title": "Research board", "url": "https://github.com/users/scholar/projects/5", "readme": "# Research index"}}
		default:
			t.Errorf("unknown GraphQL request: %s", request.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": payload})
	}))
	defer provider.Close()
	controller.GitHub.API = provider.URL
	controller.GitHub.Token = "project-token"
	c, p := call(t, srv, "POST", "/api/projects/demo/planning", testOwner, map[string]any{"github_project_id": testProjectID})
	wantOK(t, c, p)
	c, board := call(t, srv, "GET", "/api/projects/demo/planning", testOwner, nil)
	wantOK(t, c, board)
	if board["github_id"] != testProjectID || board["url"] != "https://github.com/users/scholar/projects/5" {
		t.Fatal(board)
	}
	c, items := call(t, srv, "GET", "/api/projects/demo/planning/items?after=cursor", testOwner, nil)
	wantOK(t, c, items)
	if gitObj(items["items"])["nodes"] == nil {
		t.Fatal(items)
	}
	c, fields := call(t, srv, "GET", "/api/projects/demo/planning/fields?after=page-2", testOwner, nil)
	wantOK(t, c, fields)
	if gitObj(fields["fields"])["nodes"] == nil {
		t.Fatal(fields)
	}
	c, denied := call(t, srv, "POST", "/api/projects/demo/planning/items", testOwner, map[string]any{"repository": "elsewhere/formalizations", "kind": "issue", "number": 12})
	if c == 200 {
		t.Fatalf("unlinked repository was accepted: %v", denied)
	}
	for _, link := range []struct{ repo, role string }{{"scholar/main", "source"}, {"elsewhere/formalizations", "lean"}, {"elsewhere/notes", "research"}} {
		c, v := call(t, srv, "POST", "/api/projects/demo/repositories", testOwner, map[string]any{"repository": link.repo, "role": link.role, "default_ref": "main"})
		wantOK(t, c, v)
	}
	memberships, e := db.RepositoryMemberships("demo")
	if e != nil || len(memberships) != 3 || memberships[0]["repository"] != "elsewhere/formalizations" || memberships[0]["role"] != "lean" {
		t.Fatalf("membership: %v %v", memberships, e)
	}
	repos, e := db.Repositories("demo")
	if e != nil || len(repos) != 3 {
		t.Fatalf("legacy repository links changed: %v %v", repos, e)
	}
	c, created := call(t, srv, "POST", "/api/projects/demo/planning/items", testOwner, map[string]any{"repository": "elsewhere/formalizations", "kind": "issue", "number": 12})
	wantOK(t, c, created)
	if created["item_id"] != testItemID {
		t.Fatal(created)
	}
	c, draft := call(t, srv, "POST", "/api/projects/demo/planning/drafts", testOwner, map[string]any{"title": "Test a new hypothesis", "body": "Design an experiment"})
	wantOK(t, c, draft)
	if draft["item_id"] != testItemID {
		t.Fatal(draft)
	}
	c, field := call(t, srv, "POST", "/api/projects/demo/planning/fields", testOwner, map[string]any{"item_id": testItemID, "field_id": "PVTSSF_field123", "option_id": "option12345"})
	wantOK(t, c, field)
	if field["option_id"] != "option12345" {
		t.Fatal(field)
	}
	c, _ = call(t, srv, "GET", "/api/projects/other/planning", testOwner, nil)
	if c == 200 {
		t.Fatal("board leaked to other project")
	}
	c, _ = call(t, srv, "POST", "/api/projects/demo/planning", testOwner, map[string]any{"github_project_id": "not-valid"})
	if c == 200 {
		t.Fatal("invalid board accepted")
	}
	if len(calls) < 5 {
		t.Fatalf("GraphQL calls missing: %d", len(calls))
	}
}

func TestLibraryMultiProjectImmutableSourcesAndObjects(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	if _, e := db.CreateProject("second", "Second project"); e != nil {
		t.Fatal(e)
	}
	_, e := db.CreateLibrary("lean-foundations", "Formalizations", "Reusable mathematics", "lean")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := db.LinkLibrary("demo", "lean-foundations"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.LinkLibrary("second", "lean-foundations"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.AddLibrarySource("lean-foundations", "scholar/analysis", testCommit, "Math", "formalization"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.AddLibrarySource("lean-foundations", "scholar/analysis", "main", "Math", "formalization"); e == nil {
		t.Fatal("mutable Lean revision accepted")
	}
	if _, e := db.AddLibrarySource("lean-foundations", "scholar/analysis", testCommit, "../secrets", "source"); e == nil {
		t.Fatal("path traversal accepted")
	}
	first, e := db.ResolveLibrary("demo", "lean-foundations")
	if e != nil {
		t.Fatal(e)
	}
	second, e := db.ResolveLibrary("second", "lean-foundations")
	if e != nil {
		t.Fatal(e)
	}
	if len(first["sources"].([]map[string]any)) != 1 || len(second["sources"].([]map[string]any)) != 1 {
		t.Fatal("source not shared")
	}
	if db.RepositoryLinked("demo", "scholar/analysis") {
		t.Fatal("library reference granted unrestricted project repository write")
	}
	// A binary archive larger than the old 12 MiB checkout limit stays outside Git.
	payload := bytes.Repeat([]byte("lean-olean-cache-block"), 1_000_000)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	info, e := db.StoreLibraryObject("lean-foundations", "lean-cache.tar.zst", "build-cache", digest, bytes.NewReader(payload), int64(len(payload))+1)
	if e != nil {
		t.Fatal(e)
	}
	if int64(asFloat(info["size"])) != int64(len(payload)) {
		t.Fatal(info)
	}
	_, e = db.StoreLibraryObject("lean-foundations", "lean-cache.tar.zst", "build-cache", digest, bytes.NewReader(payload), int64(len(payload))+1)
	if e == nil {
		t.Fatal("named artifact overwritten")
	}
	_, e = db.StoreLibraryObject("lean-foundations", "mismatched", "archive", strings.Repeat("f", 64), bytes.NewReader(payload), int64(len(payload))+1)
	if e == nil {
		t.Fatal("wrong content hash accepted")
	}
	_, e = db.StoreLibraryObject("lean-foundations", "oversized", "archive", digest, bytes.NewReader(payload), 1024)
	if e == nil {
		t.Fatal("size bound bypassed")
	}
	_, e = db.CreateLibrary("lean-extended", "Lean continuation", "Additional results", "lean")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.StoreLibraryObject("lean-extended", "shared-cache.tar.zst", "build-cache", digest, bytes.NewReader(payload), int64(len(payload))+1)
	if e != nil {
		t.Fatal(e)
	}
	a, _, e := db.OpenLibraryObject("lean-foundations", "lean-cache.tar.zst")
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := db.OpenLibraryObject("lean-extended", "shared-cache.tar.zst")
	if e != nil {
		a.Close()
		t.Fatal(e)
	}
	ai, _ := a.Stat()
	bi, _ := b.Stat()
	a.Close()
	b.Close()
	if !os.SameFile(ai, bi) {
		t.Fatal("identical hashes stored twice")
	}
	token, e := db.RegisterNode("worker-one", caps())
	if e != nil {
		t.Fatal(e)
	}
	_ = token
	ws, e := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", CPU: 1, MemoryMB: 512, DurationMinutes: 5})
	if e != nil {
		t.Fatal(e)
	}
	op, e := db.ClaimOperation("worker-one")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.FinishOperation("worker-one", asString(op["id"]), asString(op["lease_token"]), true, map[string]any{"status": "ready"})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := db.QueueLibraryObjectImport("demo", asString(ws["id"]), "lean-foundations", "lean-cache.tar.zst", "libraries/lean-cache.tar.zst")
	if e != nil {
		t.Fatal(e)
	}
	if asString(receipt["operation_id"]) == "" {
		t.Fatal(receipt)
	}
	artifact, e := db.ArtifactInfo("demo", asString(receipt["artifact_id"]))
	if e != nil {
		t.Fatal(e)
	}
	path, e := db.VerifyArtifact(artifact)
	if e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if !os.SameFile(stat, ai) {
		t.Fatal("workspace import copied the large object instead of linking")
	}
	otherWS, e := db.CreateWorkspace(WorkspaceRequest{ProjectID: "second", CPU: 1, MemoryMB: 512, DurationMinutes: 5})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.QueueLibraryObjectImport("demo", asString(otherWS["id"]), "lean-foundations", "lean-cache.tar.zst", "lib/cache")
	if e == nil {
		t.Fatal("cross-project workspace import accepted")
	}
}

func TestLibraryAPIRoutesAndObjectStreaming(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	c, created := call(t, srv, "POST", "/api/libraries", testOwner, map[string]any{"id": "shared-lib", "title": "Research", "description": "Library", "kind": "research"})
	wantOK(t, c, created)
	c, linked := call(t, srv, "POST", "/api/projects/demo/libraries", testOwner, map[string]any{"library": "shared-lib"})
	wantOK(t, c, linked)
	data := []byte("Research and mathematical objects should remain independent of Git.")
	hash := sha256.Sum256(data)
	sha := hex.EncodeToString(hash[:])
	req, _ := http.NewRequest("PUT", srv.URL+"/api/libraries/shared-lib/objects/report.pdf", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+testOwner)
	req.Header.Set("X-Content-SHA256", sha)
	req.Header.Set("X-Object-Kind", "paper")
	result, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	result.Body.Close()
	if result.StatusCode != 200 {
		t.Fatalf("upload failed %d", result.StatusCode)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/libraries/shared-lib/objects/report.pdf", nil)
	req.Header.Set("Authorization", "Bearer "+testOwner)
	response, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	buf, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || !bytes.Equal(buf, data) || response.Header.Get("X-Content-SHA256") != sha {
		t.Fatalf("download corrupted: %v", e)
	}
	c, library := call(t, srv, "GET", "/api/projects/demo/libraries/shared-lib/resolve", testOwner, nil)
	wantOK(t, c, library)
	if library["objects"] == nil {
		t.Fatal(library)
	}
	_, e = db.Library("shared-lib")
	if e != nil {
		t.Fatal(e)
	}
	unauthorizedReq, _ := http.NewRequest("GET", srv.URL+"/api/libraries/shared-lib/objects/report.pdf", nil)
	denied, e := srv.Client().Do(unauthorizedReq)
	if e != nil {
		t.Fatal(e)
	}
	denied.Body.Close()
	if denied.StatusCode != 401 {
		t.Fatal("library object exposed without authorization")
	}
}

func TestGitHubProjectsRejectsUntrustedResponses(t *testing.T) {
	providers := []string{"http://github.com/users/who/projects/2", "https://other.example/users/who/projects/2", "https://github.com/users/who/projects/2?foo=bar"}
	for _, location := range providers {
		if githubProjectURL(location) {
			t.Fatalf("untrusted GitHub Project URL accepted: %s", location)
		}
	}
	if !githubProjectURL("https://github.com/orgs/open-source/projects/200") {
		t.Fatal("valid organization project rejected")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Resource not accessible by integration"}}})
	}))
	defer provider.Close()
	gh := NewGitHub("test-token")
	gh.API = provider.URL
	_, e := gh.ProjectMetadata(testProjectID)
	if e == nil || !strings.Contains(e.Error(), "Resource not accessible") {
		t.Fatalf("expected explicit GitHub permission failure, got %v", e)
	}
	if _, e := gh.ProjectAddIssue(testProjectID, "acme/repo", "bad", 1); e == nil {
		t.Fatal("invalid issue kind accepted")
	}
	if _, e := gh.ProjectSetSingleSelect(testProjectID, testItemID, "bad", "bad"); e == nil {
		t.Fatal("invalid field node IDs accepted")
	}
}

func TestStreamedArtifactPreservesLargeInputsAndRespectsLimit(t *testing.T) {
	db, _, _ := fixture(t)
	if _, e := db.CreateProject("demo", "Demo"); e != nil {
		t.Fatal(e)
	}
	payload := bytes.Repeat([]byte("archive-data-"), 1_300_000)
	meta, e := db.StoreArtifactStream("demo", "library-source.tar.gz", bytes.NewReader(payload), int64(len(payload))+1)
	if e != nil {
		t.Fatal(e)
	}
	path, e := db.VerifyArtifact(meta)
	if e != nil {
		t.Fatal(e)
	}
	stored, e := os.Stat(path)
	if e != nil || stored.Size() != int64(len(payload)) {
		t.Fatalf("invalid stored archive: %v", e)
	}
	_, e = db.StoreArtifactStream("demo", "reject.tar.gz", bytes.NewReader(payload), 12<<20)
	if e == nil {
		t.Fatal("stream limit failed")
	}
	_, e = db.StoreArtifactStream("missing", "reject.tar.gz", bytes.NewReader(payload), int64(len(payload))+1)
	if e == nil {
		t.Fatal("unknown project accepted")
	}
}

func TestCatalogMigrationPreservesExistingProjectRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, e := OpenDatabase(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.CreateProject("existing", "Long-lived research"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.LinkRepository("existing", "scholar/formalization"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.WriteNote("existing", "theory", "Formal proof history", 0); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"repository_metadata", "planning_boards", "library_sources", "project_libraries", "library_objects", "libraries"} {
		if e := db.exec(`DROP TABLE ` + name); e != nil {
			t.Fatal(e)
		}
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = OpenDatabase(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	read, e := db.ReadNote("existing", "theory")
	if e != nil || read["body"] != "Formal proof history" || asInt(read["version"]) != 1 {
		t.Fatalf("note lost after schema migration: %v %v", read, e)
	}
	repos, e := db.RepositoryMemberships("existing")
	if e != nil || len(repos) != 1 || repos[0]["role"] != "source" {
		t.Fatalf("old repository link lost: %v %v", repos, e)
	}
	if _, e := db.CreateLibrary("new-library", "New catalog", "", "research"); e != nil {
		t.Fatal(e)
	}
}
