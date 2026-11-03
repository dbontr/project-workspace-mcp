package controller

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// OIDCVerifier validates RS256 JWT bearer tokens issued by an administrator-
// configured OAuth/OIDC authorization server. It is a resource-server verifier,
// not a replacement for that authorization server's login and consent flows.
type OIDCVerifier struct {
	Issuer   string
	Audience string
	JWKSURL  string
	Client   *http.Client

	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	validTil time.Time
}

func configuredOIDC() (*OIDCVerifier, error) {
	issuer, audience, jwks := os.Getenv("PWM_OIDC_ISSUER"), os.Getenv("PWM_OIDC_AUDIENCE"), os.Getenv("PWM_OIDC_JWKS_URL")
	if issuer == "" && audience == "" && jwks == "" {
		return nil, nil
	}
	if issuer == "" || audience == "" || jwks == "" || len(audience) > 256 {
		return nil, errors.New("OIDC requires issuer, audience, and JWKS URL")
	}
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" || iu.User != nil || iu.Fragment != "" {
		return nil, errors.New("OIDC issuer must be an HTTPS URL")
	}
	ku, err := url.Parse(jwks)
	if err != nil || ku.Scheme != "https" || ku.Host == "" || ku.User != nil || ku.Fragment != "" {
		return nil, errors.New("OIDC JWKS must be an HTTPS URL")
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &OIDCVerifier{Issuer: issuer, Audience: audience, JWKSURL: jwks, Client: client}, nil
}

func (v *OIDCVerifier) key(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys != nil && time.Now().Before(v.validTil) {
		if key := v.keys[kid]; key != nil {
			return key, nil
		}
		return nil, errors.New("OIDC signing key not found")
	}
	client := v.Client
	if client == nil {
		return nil, errors.New("OIDC HTTP client not configured")
	}
	response, err := client.Get(v.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC key retrieval failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("OIDC key service unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &document); err != nil || len(document.Keys) > 32 {
		return nil, errors.New("invalid OIDC JWKS")
	}
	next := make(map[string]*rsa.PublicKey)
	for _, entry := range document.Keys {
		if entry.Kty != "RSA" || entry.Kid == "" || entry.N == "" || entry.E == "" ||
			(entry.Alg != "" && entry.Alg != "RS256") || (entry.Use != "" && entry.Use != "sig") {
			continue
		}
		n, nErr := base64.RawURLEncoding.DecodeString(entry.N)
		e, eErr := base64.RawURLEncoding.DecodeString(entry.E)
		if nErr != nil || eErr != nil || len(n) < 256 || len(n) > 512 || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := new(big.Int).SetBytes(e)
		if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 65537 || exponent.Int64()%2 == 0 {
			continue
		}
		next[entry.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}
	}
	if len(next) == 0 {
		return nil, errors.New("OIDC JWKS has no supported signing keys")
	}
	v.keys, v.validTil = next, time.Now().Add(5*time.Minute)
	if key := next[kid]; key != nil {
		return key, nil
	}
	return nil, errors.New("OIDC signing key not found")
}

func jwtAudienceMatches(value any, expected string) bool {
	switch v := value.(type) {
	case string:
		return v == expected
	case []any:
		for _, element := range v {
			if element == expected {
				return true
			}
		}
	}
	return false
}

func (v *OIDCVerifier) Verify(token string) (string, error) {
	if v == nil || len(token) == 0 || len(token) > 16<<10 {
		return "", errors.New("invalid OIDC bearer token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid JWT structure")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("invalid JWT header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Alg != "RS256" || header.Kid == "" || len(header.Kid) > 256 {
		return "", errors.New("unsupported JWT signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid JWT claims")
	}
	var claims map[string]any
	if json.Unmarshal(body, &claims) != nil {
		return "", errors.New("invalid JWT claims")
	}
	issuer, subject := asString(claims["iss"]), asString(claims["sub"])
	expiry, hasExpiry := claims["exp"].(float64)
	now := float64(time.Now().Unix())
	if issuer != v.Issuer || !jwtAudienceMatches(claims["aud"], v.Audience) ||
		!hasExpiry || expiry <= now || expiry > now+86400 ||
		subject == "" || len(subject) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
		return "", errors.New("invalid JWT issuer, audience, subject, or lifetime")
	}
	if nbf, ok := claims["nbf"].(float64); ok && nbf > now+30 {
		return "", errors.New("JWT is not yet valid")
	}
	if iat, ok := claims["iat"].(float64); ok && iat > now+30 {
		return "", errors.New("JWT issued in the future")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("invalid JWT signature encoding")
	}
	key, err := v.key(header.Kid)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature) != nil {
		return "", errors.New("invalid JWT signature")
	}
	// Include issuer in the identity to prevent ambiguous grants across providers.
	return "oidc:" + v.Issuer + "#" + subject, nil
}
