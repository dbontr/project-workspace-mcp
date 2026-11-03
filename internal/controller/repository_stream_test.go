package controller

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type archiveTransport struct {
	base http.RoundTripper
	data []byte
	t    *testing.T
}

func (x archiveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "codeload.github.com" {
		if r.Header.Get("Authorization") != "" {
			x.t.Error("GitHub token was forwarded to archive host")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(x.data)), Request: r}, nil
	}
	return x.base.RoundTrip(r)
}

func TestGitHubStreamingLargePinnedLibraryCheckout(t *testing.T) {
	archive := bytes.Repeat([]byte("project-source-archive"), 850_000)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scope-token" {
			t.Errorf("GitHub auth missing")
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/repos/maths/lean/commits/" + testCommit:
			fmt.Fprint(w, `{"sha":"`+testCommit+`"}`)
		case "/repos/maths/lean/tarball/" + testCommit:
			http.Redirect(w, r, "https://codeload.github.com/maths/lean/tar.gz/"+testCommit, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	db, s, srv := fixture(t)
	project(t, srv)
	s.GitHub.API = provider.URL
	s.GitHub.Token = "scope-token"
	s.GitHub.Client.Transport = archiveTransport{base: http.DefaultTransport, data: archive, t: t}
	_, e := db.CreateLibrary("lean-library", "Lean mathematics", "", "lean")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.LinkLibrary("demo", "lean-library")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.AddLibrarySource("lean-library", "maths/lean", testCommit, "Math", "formalization")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.RegisterNode("worker-one", caps())
	if e != nil {
		t.Fatal(e)
	}
	ws, e := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", CPU: 1, MemoryMB: 512, DurationMinutes: 5})
	if e != nil {
		t.Fatal(e)
	}
	started, e := db.ClaimOperation("worker-one")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.FinishOperation("worker-one", asString(started["id"]), asString(started["lease_token"]), true, map[string]any{"status": "ready"})
	if e != nil {
		t.Fatal(e)
	}
	checkout, e := s.CheckoutLibrarySource("demo", asString(ws["id"]), "lean-library", "maths/lean", "Math")
	if e != nil {
		t.Fatal(e)
	}
	if checkout["revision"] != testCommit || checkout["status"] != "queued" {
		t.Fatalf("checkout: %v", checkout)
	}
	artifact, e := db.ArtifactInfo("demo", asString(checkout["artifact_id"]))
	if e != nil {
		t.Fatal(e)
	}
	if int64(asFloat(artifact["size"])) <= 12<<20 {
		t.Fatal("archive was truncated by legacy limit")
	}
	path, e := db.VerifyArtifact(artifact)
	if e != nil {
		t.Fatal(e)
	}
	file, e := readBounded(path, int64(len(archive))+1)
	if e != nil || !bytes.Equal(file, archive) {
		t.Fatal("archive corrupted")
	}
	op, e := db.ClaimOperation("worker-one")
	if e != nil || op["action"] != "import_archive" {
		t.Fatalf("import operation %v %v", op, e)
	}
	if gitObj(op["payload"])["repository"] != "maths/lean" {
		t.Fatal("missing library repository context")
	}
	_, e = db.FinishOperation("worker-one", asString(op["id"]), asString(op["lease_token"]), true, map[string]any{"files": 1})
	if e != nil {
		t.Fatal(e)
	}
	links, e := db.WorkspaceRepositories("demo", asString(ws["id"]))
	if e != nil || len(links) != 1 || links[0]["revision"] != testCommit {
		t.Fatalf("pinned checkout not recorded: %v %v", links, e)
	}
	if db.RepositoryLinked("demo", "maths/lean") {
		t.Fatal("checkout implicitly granted GitHub repository write access")
	}
}
