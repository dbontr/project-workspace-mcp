package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubAppReadinessChecksScopedPermissions(t *testing.T) {
	keyFile := appTestKey(t)
	tokens := map[string]string{}
	issued := map[string]int{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/app/installations/77/access_tokens" && r.Method == http.MethodPost:
			var request struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
				len(request.Repositories) != 1 {
				t.Errorf("installation token request was not repository-scoped: %v %v", request, err)
				http.Error(w, "invalid request", 400)
				return
			}
			scope := request.Repositories[0]
			if scope != "alpha" && scope != "beta" {
				t.Errorf("unknown repository scope: %s", scope)
				http.Error(w, "unexpected scope", 400)
				return
			}
			issued[scope]++
			token := fmt.Sprintf("ghs-%s-%020d", scope, issued[scope])
			tokens[scope] = token
			permissions := map[string]string{
				"contents": "write", "issues": "write",
				"pull_requests": "write", "checks": "read", "statuses": "read",
			}
			if scope == "beta" {
				permissions["contents"] = "read"
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": token, "expires_at": time.Now().Add(40 * time.Minute).UTC().Format(time.RFC3339),
				"permissions": permissions,
			})
		case strings.HasPrefix(r.URL.Path, "/repos/example/") && r.Method == http.MethodGet:
			scope := strings.TrimPrefix(r.URL.Path, "/repos/example/")
			if scope != "alpha" && scope != "beta" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+tokens[scope] {
				t.Error("wrong scoped installation token used")
				http.Error(w, "invalid credential", 401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name": "example/" + scope, "default_branch": "main",
			})
		default:
			t.Errorf("unexpected GitHub endpoint: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected endpoint", 404)
		}
	}))
	defer provider.Close()

	g := NewGitHub("")
	g.API = provider.URL
	if err := g.ConfigureApp(42, 77, keyFile); err != nil {
		t.Fatal(err)
	}
	readiness, err := g.AppReadiness([]string{"example/alpha", "example/beta"})
	if err != nil {
		t.Fatal(err)
	}
	if readiness["app_configured"] != true || readiness["ready"] != false ||
		readiness["repository_count"] != 2 {
		t.Fatalf("unexpected installation preflight: %v", readiness)
	}
	repositories, ok := readiness["repositories"].([]map[string]any)
	if !ok || len(repositories) != 2 {
		t.Fatalf("missing repository diagnostics: %v", readiness)
	}
	if repositories[0]["read_verified"] != true ||
		repositories[0]["permissions_verified"] != true ||
		len(repositories[0]["missing_permissions"].([]string)) != 0 {
		t.Fatalf("healthy repository rejected: %v", repositories[0])
	}
	if got := repositories[1]["missing_permissions"].([]string);
		len(got) != 1 || got[0] != "contents:write" {
		t.Fatalf("missing or incorrect permission deficiency: %v", got)
	}
	if issued["alpha"] != 1 || issued["beta"] != 1 {
		t.Fatalf("unexpected installation token request count: %v", issued)
	}
	data := encode(readiness)
	if strings.Contains(data, tokens["alpha"]) || strings.Contains(data, tokens["beta"]) ||
		strings.Contains(data, "PRIVATE KEY") {
		t.Fatal("readiness response exposed credentials")
	}
	if _, err := g.AppReadiness([]string{"example/alpha", "bad/repo/extra"}); err == nil {
		t.Fatal("invalid linked repository accepted")
	}
	_, _ = g.AppReadiness([]string{"example/alpha"})
}

func TestGitHubAppReadinessRequiresAdministrator(t *testing.T) {
	db, server, srv := fixture(t)
	project(t, srv)
	if _, err := db.LinkRepository("demo", "example/repo"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace",
		"viewer", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#viewer", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"jsonrpc": "2.0", "id": 12, "method": "tools/call",
		"params": map[string]any{
			"name": "github_app_readiness", "arguments": map[string]any{"project_id": "demo"},
		},
	}
	code, denied := call(t, srv, http.MethodPost, "/mcp", viewer, args)
	wantOK(t, code, denied)
	if gitObj(denied["result"])["isError"] != true {
		t.Fatalf("viewer accessed installation diagnostics: %v", denied)
	}
	code, owner := call(t, srv, http.MethodPost, "/mcp", testOwner, args)
	wantOK(t, code, owner)
	reply := gitObj(owner["result"])
	if reply["isError"] != false {
		t.Fatalf("owner diagnostics failed: %v", owner)
	}
	details := gitObj(reply["structuredContent"])
	if details["app_configured"] != false || details["repository_count"] != float64(1) {
		t.Fatalf("unconfigured installation returned incorrect status: %v", details)
	}
}
