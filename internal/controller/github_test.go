package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubRejectsRedirectAndGenericWriteByDefault(t *testing.T) {
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("redirect forwarded token to another host")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer redirected.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL+"/capture", http.StatusFound)
	}))
	defer upstream.Close()
	gh := NewGitHub("test-private-credential")
	gh.API = upstream.URL
	if _, err := gh.Repo("example/repo"); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("expected redirect to be rejected, got %v", err)
	}
	if _, err := gh.RepositoryAPI("example/repo", "DELETE", "issues/3", nil, nil); err == nil {
		t.Fatal("generic writes must require opt in")
	}
	if _, err := gh.RepositoryAPI("example/repo", "GET", "issues/../secrets", nil, nil); err == nil {
		t.Fatal("repository escape accepted")
	}
}

func TestGitHubPublicationRejectsStaleRevision(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	var writes int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing credential")
		}
		if r.Method != "GET" {
			writes++
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/example/repo/git/ref/heads/main" {
			fmt.Fprint(w, `{"object":{"sha":"`+sha+`"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	gh := NewGitHub("test-token")
	gh.API = provider.URL
	_, err := gh.Publish("example/repo", "main", strings.Repeat("f", 40), map[string][]byte{"out.txt": []byte("data")}, "One change", "")
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected stale-revision conflict, got %v", err)
	}
	if writes != 0 {
		t.Fatalf("publication created %d resources on stale revision", writes)
	}
}

func TestGitHubRepositoryAPIReadScopedToRepository(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/example/repo/pulls/12/reviews" || r.URL.Query().Get("per_page") != "20" {
			t.Errorf("incorrect repository-scoped read: %v", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true})
	}))
	defer provider.Close()
	gh := NewGitHub("test-token")
	gh.API = provider.URL
	result, err := gh.RepositoryAPI("example/repo", "GET", "pulls/12/reviews", map[string]any{"per_page": 20}, nil)
	if err != nil || gitObj(result)["valid"] != true {
		t.Fatalf("unexpected scoped read %v: %v", result, err)
	}
}
