package controller

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// configuredAccessVerifier binds Access identity to the protected application's
// issuer, audience, signing keys, and explicitly authorized owner.
func configuredAccessVerifier() (*OIDCVerifier, string, error) {
	issuer := strings.TrimRight(os.Getenv("PWM_ACCESS_ISSUER"), "/")
	audience := os.Getenv("PWM_ACCESS_AUDIENCE")
	jwks := os.Getenv("PWM_ACCESS_JWKS_URL")
	owner := strings.ToLower(strings.TrimSpace(os.Getenv("PWM_ACCESS_OWNER_EMAIL")))
	if issuer == "" && audience == "" && jwks == "" && owner == "" {
		return nil, "", nil
	}
	if issuer == "" || audience == "" || jwks == "" ||
		len(audience) > 256 || !validAccessEmail(owner) {
		return nil, "", errors.New("Cloudflare Access requires issuer, audience, JWKS URL, and authorized owner email")
	}
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" || iu.User != nil || iu.Fragment != "" {
		return nil, "", errors.New("Cloudflare Access issuer must be an HTTPS URL")
	}
	ku, err := url.Parse(jwks)
	if err != nil || ku.Scheme != "https" || ku.Host == "" || ku.User != nil || ku.Fragment != "" {
		return nil, "", errors.New("Cloudflare Access JWKS must be an HTTPS URL")
	}
	if iu.Host != ku.Host {
		return nil, "", errors.New("Cloudflare Access issuer and JWKS hosts must match")
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &OIDCVerifier{Issuer: issuer, Audience: audience, JWKSURL: jwks, Client: client}, owner, nil
}

func validAccessEmail(email string) bool {
	return email != "" && len(email) <= 320 && strings.Count(email, "@") == 1 &&
		!strings.ContainsAny(email, "\x00\r\n\t ")
}

// VerifiedEmail only reads identity claims after checking the JWT signature,
// issuer, audience, expiration and subject.
func (v *OIDCVerifier) VerifiedEmail(assertion string) (string, error) {
	if _, err := v.Verify(assertion); err != nil {
		return "", err
	}
	parts := strings.Split(assertion, ".")
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid Cloudflare Access identity encoding")
	}
	var data struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(claims, &data); err != nil {
		return "", errors.New("invalid Cloudflare Access identity")
	}
	email := strings.ToLower(strings.TrimSpace(data.Email))
	if !validAccessEmail(email) {
		return "", errors.New("Cloudflare Access email is missing or invalid")
	}
	return email, nil
}
