package controller

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signedTestToken(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject string, expiry int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key","typ":"JWT"}`))
	body, err := json.Marshal(map[string]any{
		"iss": issuer, "aud": audience, "sub": subject,
		"iat": time.Now().Unix(), "exp": expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := header + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func testOIDCIssuer(t *testing.T, server *Server) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}))
	t.Cleanup(issuer.Close)
	server.OIDC = &OIDCVerifier{
		Issuer: issuer.URL, Audience: "project-workspace", JWKSURL: issuer.URL + "/keys",
		Client: issuer.Client(),
	}
	return key, issuer.URL
}

func TestOIDCProjectPermissions(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.WriteNote("demo", "secret", "version one", 0); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	token := signedTestToken(t, key, issuer, "project-workspace", "researcher", time.Now().Add(time.Hour).Unix())
	principal := "oidc:" + issuer + "#researcher"

	code, _ := call(t, httpServer, "GET", "/api/projects/demo/notes/secret", token, nil)
	if code != 403 {
		t.Fatalf("ungranted user must not read project, got %d", code)
	}
	code, grant := call(t, httpServer, "POST", "/api/projects/demo/permissions", testOwner,
		map[string]any{"principal": principal, "role": "viewer", "expected_role": ""})
	wantOK(t, code, grant)
	code, note := call(t, httpServer, "GET", "/api/projects/demo/notes/secret", token, nil)
	wantOK(t, code, note)
	if asString(note["body"]) != "version one" {
		t.Fatalf("wrong project note: %v", note)
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/demo/notes/secret", token,
		map[string]any{"body": "should fail", "expected_version": 1})
	if code != 403 {
		t.Fatal("viewer must not modify a project")
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/demo/permissions", testOwner,
		map[string]any{"principal": principal, "role": "editor", "expected_role": "viewer"})
	if code != 200 {
		t.Fatal("grant update must succeed")
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/demo/notes/secret", token,
		map[string]any{"body": "version two", "expected_version": 1})
	if code != 200 {
		t.Fatal("editor should be able to update their project")
	}
	code, _ = call(t, httpServer, "POST", "/api/projects/demo/permissions", testOwner,
		map[string]any{"principal": principal, "role": "revoke", "expected_role": "editor"})
	if code != 200 {
		t.Fatal("grant revocation failed")
	}
	code, _ = call(t, httpServer, "GET", "/api/projects/demo/notes/secret", token, nil)
	if code != 403 {
		t.Fatal("revoked user must immediately lose project access")
	}
	wrong := signedTestToken(t, key, issuer, "different-audience", "researcher", time.Now().Add(time.Hour).Unix())
	code, _ = call(t, httpServer, "GET", "/api/whoami", wrong, nil)
	if code != 401 {
		t.Fatal("tokens with a different audience must be rejected")
	}
	expired := signedTestToken(t, key, issuer, "project-workspace", "researcher", time.Now().Add(-time.Hour).Unix())
	code, _ = call(t, httpServer, "GET", "/api/whoami", expired, nil)
	if code != 401 {
		t.Fatal("expired bearer tokens must be rejected")
	}
}

func TestOIDCMCPAuthorizationAndIsolation(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other-project", "Other"); err != nil {
		t.Fatal(err)
	}
	key, issuer := testOIDCIssuer(t, server)
	token := signedTestToken(t, key, issuer, "project-workspace", "learner", time.Now().Add(time.Hour).Unix())
	principal := "oidc:" + issuer + "#learner"
	if _, err := db.SetProjectGrant("demo", principal, "viewer", ""); err != nil {
		t.Fatal(err)
	}
	mcpCall := func(name, project string) bool {
		t.Helper()
		code, response := call(t, httpServer, "POST", "/mcp/", token, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": map[string]any{"project_id": project, "name": "test-note"}},
		})
		wantOK(t, code, response)
		result, _ := response["result"].(map[string]any)
		return asBool(result["isError"])
	}
	if !mcpCall("write_project_note", "demo") {
		t.Fatal("MCP viewer cannot write")
	}
	if !mcpCall("read_project_note", "other-project") {
		t.Fatal("MCP viewer cannot read another project")
	}
	if !mcpCall("github_repository_api", "demo") {
		t.Fatal("shared GitHub credential must remain owner-only")
	}
	if _, err := db.SetProjectGrant("demo", principal, "owner", "viewer"); err != nil {
		t.Fatal(err)
	}
	if mcpCall("workspace_list", "demo") {
		t.Fatal("project member should be allowed to list their workspace records")
	}
}
