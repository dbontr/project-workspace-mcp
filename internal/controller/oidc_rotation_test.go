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
	"sync/atomic"
	"testing"
	"time"
)

func signOIDCTestToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestOIDCSigningKeyRotationAndRefreshLimit(t *testing.T) {
	older, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	type signingKey struct {
		kid string
		key *rsa.PrivateKey
	}
	var current atomic.Value
	current.Store(signingKey{kid: "older", key: older})
	var fetched atomic.Int32
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.NotFound(w, r)
			return
		}
		fetched.Add(1)
		selected := current.Load().(signingKey)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "kid": selected.kid, "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(selected.key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(selected.key.PublicKey.E)).Bytes()),
		}}})
	}))
	defer issuer.Close()
	verifier := &OIDCVerifier{Issuer: issuer.URL, Audience: "project-workspace",
		JWKSURL: issuer.URL + "/keys", Client: issuer.Client()}
	claims := map[string]any{"iss": issuer.URL, "aud": "project-workspace",
		"sub": "researcher", "exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix()}
	if _, err := verifier.Verify(signOIDCTestToken(t, older, "older", claims)); err != nil {
		t.Fatalf("first issuer key failed: %v", err)
	}
	current.Store(signingKey{kid: "newer", key: newer})
	next := signOIDCTestToken(t, newer, "newer", claims)
	if _, err := verifier.Verify(next); err == nil {
		t.Fatal("accepted an unrecognized key before fetching the issuer")
	}
	if n := fetched.Load(); n != 1 {
		t.Fatalf("unrecognized key bypassed JWKS refresh limit: %d", n)
	}
	// Simulate expiry of the refresh cooldown without delaying the test.
	verifier.mu.Lock()
	verifier.lastFetch = time.Now().Add(-time.Minute)
	verifier.mu.Unlock()
	if _, err := verifier.Verify(next); err != nil {
		t.Fatalf("rotated issuer key was not loaded: %v", err)
	}
	if n := fetched.Load(); n != 2 {
		t.Fatalf("expected one bounded key refresh, got %d", n)
	}
	for i := 0; i < 10; i++ {
		if _, err := verifier.Verify(signOIDCTestToken(t, newer, "unknown-kid", claims)); err == nil {
			t.Fatal("unrecognized signing key was accepted")
		}
	}
	if n := fetched.Load(); n != 2 {
		t.Fatalf("unknown key IDs caused excessive JWKS requests: %d", n)
	}
}

func TestOIDCRejectsNonNumericTimeClaims(t *testing.T) {
	_, server, _ := fixture(t)
	key, issuer := testOIDCIssuer(t, server)
	claims := map[string]any{
		"iss": issuer, "aud": "project-workspace", "sub": "subject",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	for _, field := range []string{"iat", "nbf"} {
		claims[field] = "not-a-number"
		token := signOIDCTestToken(t, key, "test-key", claims)
		if _, err := server.OIDC.Verify(token); err == nil {
			t.Fatalf("accepted nonnumeric JWT %s", field)
		}
		delete(claims, field)
	}
}
