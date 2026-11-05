package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKnowledgePolicyRequiresExplicitGitMode(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	initial, err := db.KnowledgePolicy("research")
	if err != nil || initial["mode"] != "none" || asInt(initial["version"]) != 0 {
		t.Fatalf("default knowledge mode: %v %v", initial, err)
	}
	if _, err := db.ConfigureKnowledgePolicy("research", "git", "example/notes", "main",
		"knowledge", 1); err == nil {
		t.Fatal("stale initial revision accepted")
	}
	enabled, err := db.ConfigureKnowledgePolicy("research", "git",
		"example/notes", "main", "knowledge", 0)
	if err != nil || enabled["mode"] != "git" {
		t.Fatalf("configure Git knowledge: %v %v", enabled, err)
	}
	if _, err := db.ConfigureKnowledgePolicy("research", "git",
		"example/notes", "main", "../escape", 1); err == nil {
		t.Fatal("path traversal accepted")
	}
	disabled, err := db.ConfigureKnowledgePolicy("research", "none", "", "", "", 1)
	if err != nil || disabled["mode"] != "none" {
		t.Fatalf("disable knowledge: %v %v", disabled, err)
	}
}

func TestGitKnowledgePublicationVerifiesRemoteCommitAndContent(t *testing.T) {
	const prior = "1111111111111111111111111111111111111111"
	const tree = "2222222222222222222222222222222222222222"
	const blob = "3333333333333333333333333333333333333333"
	const nextTree = "4444444444444444444444444444444444444444"
	const next = "5555555555555555555555555555555555555555"
	const body = "# State\n\nVerified results.\n"
	changed := false
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/user":
			fmt.Fprint(w, `{"id":123,"login":"author","type":"User"}`)
		case r.Method == "GET" && r.URL.Path == "/repos/example/notes/git/ref/heads/main":
			sha := prior
			if changed {
				sha = next
			}
			fmt.Fprintf(w, `{"object":{"sha":"%s"}}`, sha)
		case r.Method == "GET" && r.URL.Path == "/repos/example/notes/git/commits/"+prior:
			fmt.Fprintf(w, `{"tree":{"sha":"%s"}}`, tree)
		case r.Method == "POST" && r.URL.Path == "/repos/example/notes/git/blobs":
			fmt.Fprintf(w, `{"sha":"%s"}`, blob)
		case r.Method == "POST" && r.URL.Path == "/repos/example/notes/git/trees":
			fmt.Fprintf(w, `{"sha":"%s"}`, nextTree)
		case r.Method == "POST" && r.URL.Path == "/repos/example/notes/git/commits":
			fmt.Fprintf(w, `{"sha":"%s"}`, next)
		case r.Method == "PATCH" && r.URL.Path == "/repos/example/notes/git/refs/heads/main":
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["sha"] != next || request["force"] != false {
				t.Errorf("unsafe Git ref update: %v", request)
			}
			changed = true
			updates++
			fmt.Fprintf(w, `{"object":{"sha":"%s"}}`, next)
		case r.Method == "GET" && r.URL.Path == "/repos/example/notes/contents/knowledge/Context.md":
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","sha":"%s","content":"%s"}`,
				blob, base64.StdEncoding.EncodeToString([]byte(body)))
		default:
			t.Errorf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	g := NewGitHub("test-user-token")
	g.API = server.URL
	if _, err := g.CommitKnowledgeDocument("example/notes", "main",
		"knowledge/Context.md", "wrong", body, "Update context"); err == nil {
		t.Fatal("accepted malformed base revision")
	}
	result, err := g.CommitKnowledgeDocument("example/notes", "main",
		"knowledge/Context.md", prior, body, "Update context")
	if err != nil || result["verified"] != true || result["sha"] != next || updates != 1 {
		t.Fatalf("publication not verified: %v %v", result, err)
	}
	if _, err := g.CommitKnowledgeDocument("example/notes", "main",
		"knowledge/Context.md", prior, body, "Update context"); err == nil {
		t.Fatal("stale knowledge commit accepted")
	}
}
