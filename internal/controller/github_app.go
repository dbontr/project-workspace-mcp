package controller

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type appCredential struct {
	token   string
	expires time.Time
}

type GitHubApp struct {
	AppID          int64
	InstallationID int64
	Key            *rsa.PrivateKey

	mu     sync.Mutex
	cached map[string]appCredential
}

func parseGithubAppKey(filename string) (*rsa.PrivateKey, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("GitHub App signing key must be a regular owner-only file")
	}
	pemData, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("invalid GitHub App private key PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var decoded any
		decoded, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = decoded.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("GitHub App requires an RSA private key")
			}
		}
	default:
		return nil, errors.New("unsupported GitHub App private key type")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid or weak GitHub App signing key")
	}
	return key, nil
}

func (g *GitHub) ConfigureApp(appID, installationID int64, keyFile string) error {
	if appID <= 0 || installationID <= 0 || keyFile == "" {
		return errors.New("invalid GitHub App configuration")
	}
	key, err := parseGithubAppKey(keyFile)
	if err != nil {
		return err
	}
	g.App = &GitHubApp{AppID: appID, InstallationID: installationID, Key: key, cached: map[string]appCredential{}}
	return nil
}

func configureGitHubAppFromEnv(g *GitHub) error {
	id := os.Getenv("PWM_GITHUB_APP_ID")
	installation := os.Getenv("PWM_GITHUB_INSTALLATION_ID")
	key := os.Getenv("PWM_GITHUB_APP_KEY_FILE")
	if id == "" && installation == "" && key == "" {
		return nil
	}
	appID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("invalid GitHub App ID")
	}
	installationID, err := strconv.ParseInt(installation, 10, 64)
	if err != nil {
		return errors.New("invalid GitHub installation ID")
	}
	return g.ConfigureApp(appID, installationID, key)
}

func (a *GitHubApp) jwt() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body, err := json.Marshal(map[string]any{
		"iss": strconv.FormatInt(a.AppID, 10),
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(unsigned))
	signed, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signed), nil
}

// githubTokenScope returns a repository name for REST resources so a leaked
// installation token cannot read other repositories in the installation.
// GraphQL needs project-wide access to linked repositories and uses the
// installation's configured repository selection instead.
func githubTokenScope(requestPath string) (string, error) {
	if !strings.HasPrefix(requestPath, "/repos/") {
		return "", nil
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/repos/"), "/")
	if len(parts) < 2 || !validGitRepository(parts[0]+"/"+parts[1]) {
		return "", errors.New("invalid scoped GitHub repository path")
	}
	return parts[1], nil
}

func (g *GitHub) credential(requestPath string) (string, error) {
	if g.Token != "" {
		return g.Token, nil
	}
	if g.App == nil {
		return "", errors.New("GitHub credential is not configured")
	}
	scope, err := githubTokenScope(requestPath)
	if err != nil {
		return "", err
	}
	return g.App.installationToken(g, scope)
}

func (a *GitHubApp) installationToken(g *GitHub, scope string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cached, ok := a.cached[scope]; ok && time.Now().Add(2*time.Minute).Before(cached.expires) {
		return cached.token, nil
	}
	appJWT, err := a.jwt()
	if err != nil {
		return "", err
	}
	base, err := url.Parse(g.API)
	if err != nil {
		return "", err
	}
	endpoint := *base
	endpoint.Path = path.Join("/", "app", "installations", strconv.FormatInt(a.InstallationID, 10), "access_tokens")
	endpoint.RawQuery, endpoint.Fragment = "", ""
	data := map[string]any{}
	if scope != "" {
		data["repositories"] = []string{scope}
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "project-workspace-mcp")
	client := *g.Client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	resp, err := io.ReadAll(io.LimitReader(res.Body, 128<<10))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub App installation token HTTP %d", res.StatusCode)
	}
	var item struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp, &item); err != nil {
		return "", err
	}
	expires, err := time.Parse(time.RFC3339, item.ExpiresAt)
	if err != nil || len(item.Token) < 20 || !expires.After(time.Now().Add(2*time.Minute)) {
		return "", errors.New("invalid GitHub App installation credential response")
	}
	a.cached[scope] = appCredential{token: item.Token, expires: expires}
	return item.Token, nil
}
