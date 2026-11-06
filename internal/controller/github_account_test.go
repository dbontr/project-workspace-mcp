package controller

import (
	"encoding/json"
	"fmt"
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

func TestAccountRepositoryArchiveAndSettings(t *testing.T) {
	archived := false
	issues := true
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/example/source" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "full_name": "example/source", "archived": archived,
				"has_issues": issues, "permissions": map[string]any{"admin": true},
			})
		case "PATCH":
			patches++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "bad JSON", http.StatusBadRequest)
				return
			}
			if value, present := body["archived"]; present {
				archived = value.(bool)
			}
			if value, present := body["has_issues"]; present {
				issues = value.(bool)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "example/source"})
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	g := NewGitHub("test-token")
	g.API = server.URL
	if _, err := g.AccountRepositoryArchive("example/source", 42, true, false); err == nil {
		t.Fatal("archive accepted without confirmation")
	}
	if _, err := g.AccountRepositoryArchive("example/source", 43, true, true); err == nil {
		t.Fatal("archive accepted stale repository ID")
	}
	if _, err := g.AccountRepositoryConfigure("example/source", 42, map[string]any{"archived": true}, true); err == nil {
		t.Fatal("unsupported setting accepted")
	}
	if _, err := g.AccountRepositoryConfigure("example/source", 42, map[string]any{"has_issues": "false"}, true); err == nil {
		t.Fatal("invalid setting type accepted")
	}
	if patches != 0 {
		t.Fatalf("invalid operations mutated repository: %d", patches)
	}
	result, err := g.AccountRepositoryArchive("example/source", 42, true, true)
	if err != nil || result["verified"] != true || result["archived"] != true {
		t.Fatalf("archive: %v %v", result, err)
	}
	result, err = g.AccountRepositoryArchive("example/source", 42, false, true)
	if err != nil || result["verified"] != true || result["archived"] != false {
		t.Fatalf("unarchive: %v %v", result, err)
	}
	settings, err := g.AccountRepositoryConfigure("example/source", 42, map[string]any{"has_issues": false}, true)
	if err != nil || settings["has_issues"] != false {
		t.Fatalf("settings: %v %v", settings, err)
	}
	if patches != 3 {
		t.Fatalf("expected three state changes, got %d", patches)
	}
}

func TestAccountAdministrationRequiresControllerOwner(t *testing.T) {
	for _, name := range []string{"github_account_settings", "github_account_configure", "github_account_archive"} {
		if !rootTools[name] {
			t.Fatalf("%s must require controller-owner authorization", name)
		}
	}
}
