package controller

import (
	"fmt"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountRepositoryChangesRequireIdentityAndPermission(t *testing.T) {
	admin := true
	renamed, transferred := false, false
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		path := r.URL.Path
		switch {
		case r.Method == "GET" && path == "/repos/example/source":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "example/source",
				"permissions": map[string]any{"admin": admin}})
		case r.Method == "PATCH" && path == "/repos/example/source":
			renamed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "example/target"})
		case r.Method == "POST" && path == "/repos/example/source/transfer":
			transferred = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "newowner/source"})
		case r.Method == "GET" && path == "/repos/example/target":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "example/target"})
		case r.Method == "GET" && path == "/repos/newowner/source":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "newowner/source"})
		case r.Method == "GET" && path == "/search/repositories":
			fmt.Fprint(w, `{"total_count":1,"items":[{"id":42,"full_name":"example/source","private":true}]}`)
		case r.Method == "GET" && path == "/user/repos":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"id": 42, "full_name": "example/source"}})
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	g := NewGitHub("test-token")
	g.API = server.URL
	if _, err := g.AccountRepositoryChange("example/source", "rename", "target", 42, false); err == nil {
		t.Fatal("rename accepted without confirmation")
	}
	if _, err := g.AccountRepositoryChange("example/source", "rename", "target", 43, true); err == nil {
		t.Fatal("rename accepted a stale repository identity")
	}
	admin = false
	if _, err := g.AccountRepositoryChange("example/source", "rename", "target", 42, true); err == nil {
		t.Fatal("nonadministrator renamed a repository")
	}
	if renamed || transferred {
		t.Fatal("unexpected provider mutation")
	}
	admin = true
	if _, err := g.SearchGitHubRepositories("", 1); err == nil {
		t.Fatal("accepted empty repository search")
	}
	if found, err := g.SearchGitHubRepositories("example", 1); err != nil ||
		len(found["repositories"].([]map[string]any)) != 1 {
		t.Fatalf("repository search: %v %v", found, err)
	}
	if _, err := g.AccountRepositories(0); err == nil {
		t.Fatal("accepted invalid page")
	}
	result, err := g.AccountRepositories(1)
	if err != nil || len(result.([]any)) != 1 {
		t.Fatalf("account listing: %v %v", result, err)
	}
	rename, err := g.AccountRepositoryChange("example/source", "rename", "target", 42, true)
	if err != nil || !renamed || rename["confirmed"] != true {
		t.Fatalf("rename not verified: %v %v", rename, err)
	}
	transfer, err := g.AccountRepositoryChange("example/source", "transfer", "newowner", 42, true)
	if err != nil || !transferred || transfer["confirmed"] != true {
		t.Fatalf("transfer not verified: %v %v", transfer, err)
	}
	if requests < 8 {
		t.Fatalf("expected provider verification: %d", requests)
	}
}

func TestAccountOperationsRequireControllerOwner(t *testing.T) {
	_, srv, _ := func() (*Database, *Server, *httptest.Server) {
		d, s, h := fixture(t)
		return d, s, h
	}()
	if !rootTools["github_account_rename"] || !rootTools["github_account_transfer"] {
		t.Fatal("account mutations must be root-only")
	}
	if !rootTools["github_account_repositories"] {
		t.Fatal("account enumeration must be root-only")
	}
	_ = srv
}
