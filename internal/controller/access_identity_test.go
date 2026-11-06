package controller

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func signedAccessAssertion(t *testing.T, key *rsa.PrivateKey, issuer, audience, email string, expiry int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	claims, err := json.Marshal(map[string]any{
		"iss": issuer, "aud": audience, "sub": "owner-subject", "email": email,
		"iat": time.Now().Unix(), "exp": expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestCloudflareMCPAssertionAuthorization(t *testing.T) {
	_, service, server := fixture(t)
	key, issuer := testOIDCIssuer(t, service)
	service.AccessVerifier = service.OIDC
	service.AccessOwnerEmail = "owner@example.com"
	service.AccessVerifier.Audience = "access-application"
	makeRequest := func(token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"identity-test","version":"1"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cf-Access-Jwt-Assertion", token)
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	valid := signedAccessAssertion(t, key, issuer, "access-application", "owner@example.com", time.Now().Add(time.Hour).Unix())
	if code := makeRequest(valid); code != http.StatusOK {
		t.Fatalf("signed owner assertion rejected by authentication layer: %d", code)
	}
	for name, token := range map[string]string{
		"wrong owner":    signedAccessAssertion(t, key, issuer, "access-application", "other@example.com", time.Now().Add(time.Hour).Unix()),
		"wrong audience": signedAccessAssertion(t, key, issuer, "another-audience", "owner@example.com", time.Now().Add(time.Hour).Unix()),
		"expired":       signedAccessAssertion(t, key, issuer, "access-application", "owner@example.com", time.Now().Add(-time.Minute).Unix()),
		"unsigned":      "not-a-jwt",
	} {
		if code := makeRequest(token); code != http.StatusUnauthorized {
			t.Errorf("%s assertion returned %d; expected 401", name, code)
		}
	}
}
func TestCloudflareConfigRequiresCompleteIdentity(t *testing.T) {
	t.Setenv("PWM_ACCESS_ISSUER", "https://team.cloudflareaccess.com")
	t.Setenv("PWM_ACCESS_AUDIENCE", "audience")
	t.Setenv("PWM_ACCESS_JWKS_URL", "https://another.example.com/keys")
	t.Setenv("PWM_ACCESS_OWNER_EMAIL", "owner@example.com")
	if _, _, err := configuredAccessVerifier(); err == nil {
		t.Fatal("JWKS on another host must be rejected")
	}
	t.Setenv("PWM_ACCESS_JWKS_URL", "https://team.cloudflareaccess.com/cdn-cgi/access/certs")
	if _, _, err := configuredAccessVerifier(); err != nil {
		t.Fatal(err)
	}
}
