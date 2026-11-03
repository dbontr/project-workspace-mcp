package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHubPATFilePermissionsAndPrecedence(t *testing.T) {
	t.Setenv("PWM_GITHUB_TOKEN", "legacy-token")
	private := filepath.Join(t.TempDir(), "github-pat")
	pat := strings.Repeat("p", 40)
	if err := os.WriteFile(private, []byte(pat+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWM_GITHUB_PAT_FILE", private)
	token, err := configuredGitHubPAT()
	if err != nil || token != pat {
		t.Fatalf("owner-only PAT file not selected: %v", err)
	}
	if err := os.Chmod(private, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredGitHubPAT(); err == nil {
		t.Fatal("world-readable PAT file accepted")
	}
	if err := os.Chmod(private, 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(filepath.Dir(private), "indirect")
	if err := os.Symlink(private, symlink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWM_GITHUB_PAT_FILE", symlink)
	if _, err := configuredGitHubPAT(); err == nil {
		t.Fatal("symlinked PAT file accepted")
	}
	t.Setenv("PWM_GITHUB_PAT_FILE", "relative/pat")
	if _, err := configuredGitHubPAT(); err == nil {
		t.Fatal("relative PAT file accepted")
	}
	t.Setenv("PWM_GITHUB_PAT_FILE", "")
	token, err = configuredGitHubPAT()
	if err != nil || token != "legacy-token" {
		t.Fatalf("legacy controller token compatibility: %q %v", token, err)
	}
	t.Setenv("PWM_GITHUB_PAT_FILE", private)
	t.Setenv("PWM_GITHUB_APP_ID", "invalid-app-id")
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := NewServer(db, testOwner)
	if err != nil || server.GitHub.Token != pat || server.GitHub.App != nil {
		t.Fatalf("user PAT did not take precedence over App settings: %v", err)
	}
}

func TestGitHubPATPublicationUsesAuthenticatedIdentity(t *testing.T) {
	const base = "1111111111111111111111111111111111111111"
	const created = "2222222222222222222222222222222222222222"
	token := strings.Repeat("s", 40)
	var createdAsUser, createdRef, openedPR bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("GitHub request missing the PAT: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unauthorized", 401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"`+base+`"}}`)
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/git/commits/"+base:
			fmt.Fprint(w, `{"tree":{"sha":"3333333333333333333333333333333333333333"}}`)
		case r.Method == "GET" && r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 54321, "login": "researcher", "name": "Researcher", "type": "User",
			})
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/git/blobs":
			fmt.Fprint(w, `{"sha":"4444444444444444444444444444444444444444"}`)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/git/trees":
			fmt.Fprint(w, `{"sha":"5555555555555555555555555555555555555555"}`)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/git/commits":
			var body struct {
				Message   string            `json:"message"`
				Parents   []string          `json:"parents"`
				Author    map[string]string `json:"author"`
				Committer map[string]string `json:"committer"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			want := map[string]string{
				"name": "Researcher", "email": "54321+researcher@users.noreply.github.com",
			}
			if body.Author["name"] != want["name"] || body.Author["email"] != want["email"] ||
				body.Committer["name"] != want["name"] || body.Committer["email"] != want["email"] ||
				len(body.Parents) != 1 || body.Parents[0] != base {
				t.Errorf("Git commit must use authenticated PAT account: %+v", body)
			}
			createdAsUser = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": created, "author": body.Author, "committer": body.Committer,
			})
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/git/refs":
			createdRef = true
			fmt.Fprint(w, `{"ref":"refs/heads/pwm/test"}`)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/pulls":
			openedPR = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"html_url": "https://github.com/example/repo/pull/7",
				"number":   7,
			})
		default:
			t.Errorf("unexpected GitHub API call: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected endpoint", 404)
		}
	}))
	defer provider.Close()
	g := NewGitHub(token)
	g.API = provider.URL
	published, err := g.Publish("example/repo", "main", base, map[string][]byte{
		"research.txt": []byte("evidence"),
	}, "Publish research", "Reviewed changes")
	if err != nil {
		t.Fatal(err)
	}
	result := gitObj(published)
	if !createdAsUser || !createdRef || !openedPR || result["commit_sha"] != created ||
		result["author_login"] != "researcher" {
		t.Fatalf("publication not attributed to PAT account: %v", result)
	}
	if strings.Contains(encode(result), token) {
		t.Fatal("PAT leaked into publication result")
	}
}

func TestGitHubPATPublicationFailsBeforeWritesWithoutUserIdentity(t *testing.T) {
	const base = "1111111111111111111111111111111111111111"
	var mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" || r.Method == "PATCH" || r.Method == "PUT" {
			mutations++
		}
		switch r.URL.Path {
		case "/repos/example/repo/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"`+base+`"}}`)
		case "/repos/example/repo/git/commits/" + base:
			fmt.Fprint(w, `{"tree":{"sha":"3333333333333333333333333333333333333333"}}`)
		case "/user":
			fmt.Fprint(w, `{"login":"researcher","id":0}`)
		default:
			http.Error(w, "unexpected endpoint", 404)
		}
	}))
	defer server.Close()
	g := NewGitHub("test-token")
	g.API = server.URL
	if _, err := g.Publish("example/repo", "main", base, map[string][]byte{
		"file.txt": []byte("content"),
	}, "Changes", ""); err == nil {
		t.Fatal("publication proceeded without an authenticated GitHub user ID")
	}
	if mutations != 0 {
		t.Fatalf("performed %d mutations without a verified GitHub user", mutations)
	}
}

func TestGitHubPATReadinessRespectsPermissions(t *testing.T) {
	token := strings.Repeat("t", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "invalid token", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"id":1234,"login":"researcher","type":"User"}`)
		case "/repos/example/active":
			fmt.Fprint(w, `{"full_name":"example/active","permissions":{"push":true,"admin":true}}`)
		case "/repos/example/read-only":
			fmt.Fprint(w, `{"full_name":"example/read-only","permissions":{"push":false,"admin":false}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	g := NewGitHub(token)
	g.API = server.URL
	report, err := g.PATReadiness([]string{"example/active", "example/read-only"})
	if err != nil {
		t.Fatal(err)
	}
	if report["ready"] != false || report["authenticated_login"] != "researcher" ||
		report["commit_author_email"] != "1234+researcher@users.noreply.github.com" {
		t.Fatalf("invalid PAT readiness report: %v", report)
	}
	repos := report["repositories"].([]map[string]any)
	if repos[0]["reported_admin"] != true || repos[1]["reported_push"] != false {
		t.Fatalf("invalid provider permissions: %v", repos)
	}
	if strings.Contains(encode(report), token) {
		t.Fatal("PAT disclosed in readiness response")
	}
}

func TestGitHubPATReadinessRequiresAdministrator(t *testing.T) {
	t.Setenv("PWM_GITHUB_PAT_FILE", "")
	t.Setenv("PWM_GITHUB_TOKEN", "")
	db, server, srv := fixture(t)
	project(t, srv)
	if _, err := db.LinkRepository("demo", "example/repo"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace", "viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "github_pat_readiness",
			"arguments": map[string]any{"project_id": "demo"}},
	}
	code, denied := call(t, srv, http.MethodPost, "/mcp", viewer, request)
	wantOK(t, code, denied)
	if gitObj(denied["result"])["isError"] != true {
		t.Fatalf("project viewer accessed user PAT diagnostics: %v", denied)
	}
	code, allowed := call(t, srv, http.MethodPost, "/mcp", testOwner, request)
	wantOK(t, code, allowed)
	out := gitObj(gitObj(allowed["result"])["structuredContent"])
	if out["pat_configured"] != false || out["ready"] != false {
		t.Fatalf("unexpected unconfigured PAT result: %v", allowed)
	}
}
