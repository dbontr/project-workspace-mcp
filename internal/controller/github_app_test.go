package controller

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "github-app.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filename, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func checkAppJWT(t *testing.T, jwt string, key *rsa.PublicKey) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid GitHub App JWT: %q", jwt)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil || claims["iss"] != "42" {
		t.Fatalf("wrong app JWT issuer: %v", claims)
	}
	if exp, ok := claims["exp"].(float64); !ok || exp <= float64(time.Now().Unix()) || exp > float64(time.Now().Add(10*time.Minute).Unix()) {
		t.Fatalf("invalid app JWT lifetime: %v", claims)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("bad GitHub App signature: %v", err)
	}
}

func TestGitHubAppInstallationCredentials(t *testing.T) {
	filename := appTestKey(t)
	keyData, _ := os.ReadFile(filename)
	block, _ := pem.Decode(keyData)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	issued := map[string]int{}
	seenRepo := 0
	seenGraphQL := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/app/installations/77/access_tokens" && r.Method == http.MethodPost:
			checkAppJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &key.PublicKey)
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			scope := "all"
			if repos, ok := request["repositories"].([]any); ok && len(repos) == 1 {
				scope, _ = repos[0].(string)
			}
			issued[scope]++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      fmt.Sprintf("ghs-%s-%020d", scope, issued[scope]),
				"expires_at": time.Now().Add(40 * time.Minute).UTC().Format(time.RFC3339),
			})
		case r.URL.Path == "/repos/example/repo" && r.Method == http.MethodGet:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs-repo-") {
				t.Errorf("repository REST call used an unscoped credential: %s", r.Header.Get("Authorization"))
			}
			seenRepo++
			fmt.Fprint(w, `{"full_name":"example/repo"}`)
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs-all-") {
				t.Errorf("GraphQL request used wrong installation credential")
			}
			seenGraphQL++
			fmt.Fprint(w, `{"data":{"viewer":{"login":"owner"}}}`)
		default:
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unrecognized request", 404)
		}
	}))
	defer provider.Close()
	g := NewGitHub("")
	g.API = provider.URL
	if err := g.ConfigureApp(42, 77, filename); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Repo("example/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Repo("example/repo"); err != nil {
		t.Fatal(err)
	}
	if issued["repo"] != 1 || seenRepo != 2 {
		t.Fatalf("app token should be reused: %v, requests=%d", issued, seenRepo)
	}
	if _, err := g.GraphQL("query { viewer { login } }", nil); err != nil {
		t.Fatal(err)
	}
	if issued["all"] != 1 || seenGraphQL != 1 {
		t.Fatalf("GraphQL should use installation credential: %v", issued)
	}
	g.App.mu.Lock()
	g.App.cached["repo"] = appCredential{token: "expired", expires: time.Now().Add(30 * time.Second)}
	g.App.mu.Unlock()
	if _, err := g.Repo("example/repo"); err != nil {
		t.Fatal(err)
	}
	if issued["repo"] != 2 {
		t.Fatalf("expired token must refresh before use: %v", issued)
	}
}

func TestGitHubAppRejectsUnsafeKeyPermissions(t *testing.T) {
	filename := appTestKey(t)
	if err := os.Chmod(filename, 0644); err != nil {
		t.Fatal(err)
	}
	g := NewGitHub("")
	if err := g.ConfigureApp(42, 77, filename); err == nil {
		t.Fatal("world-readable installation signing key must be rejected")
	}
	if err := g.ConfigureApp(0, 77, filename); err == nil {
		t.Fatal("missing app identity must be rejected")
	}
	if key, err := githubTokenScope("/repos/example/repo/git/refs/heads/main"); err != nil || key != "repo" {
		t.Fatalf("repository scope: %q %v", key, err)
	}
	if key, err := githubTokenScope("/graphql"); err != nil || key != "" {
		t.Fatalf("GraphQL should be unscoped: %q %v", key, err)
	}
	if _, err := githubTokenScope("/repos/example/%2Fsecret"); err == nil {
		t.Fatal("malformed repository scope must be rejected")
	}
}
