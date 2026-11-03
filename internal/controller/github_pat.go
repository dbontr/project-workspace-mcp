package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var githubAccountLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// configuredGitHubPAT accepts a controller-local, owner-only credential file.
// The legacy PWM_GITHUB_TOKEN environment variable remains supported.
func configuredGitHubPAT() (string, error) {
	location := os.Getenv("PWM_GITHUB_PAT_FILE")
	if location == "" {
		token := strings.TrimSuffix(os.Getenv("PWM_GITHUB_TOKEN"), "\n")
		if strings.ContainsAny(token, "\x00\r\n\t ") {
			return "", errors.New("invalid GitHub PAT environment value")
		}
		return token, nil
	}
	if !filepath.IsAbs(location) {
		return "", errors.New("GitHub PAT file path must be absolute")
	}
	info, err := os.Lstat(location)
	if err != nil {
		return "", fmt.Errorf("GitHub PAT file unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return "", errors.New("GitHub PAT must be a regular owner-only file")
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return "", fmt.Errorf("GitHub PAT file unreadable: %w", err)
	}
	token := strings.TrimSuffix(string(data), "\n")
	if len(token) < 20 || len(token) > 4096 || strings.ContainsAny(token, "\x00\r\n\t ") {
		return "", errors.New("GitHub PAT file contains an invalid token")
	}
	return token, nil
}

type gitCommitIdentity struct {
	Login string
	Name  string
	Email string
}

// commitIdentity resolves Git authorship from the account authenticated by
// the PAT, never from arbitrary tool arguments or a workspace's git config.
// The ID-based no-reply address associates commits with the account without
// exposing its personal email address.
func (g *GitHub) commitIdentity() (gitCommitIdentity, error) {
	if g.Token == "" {
		return gitCommitIdentity{}, errors.New("user-owned GitHub PAT required for commit publication")
	}
	response, err := g.request("GET", "/user", nil, nil)
	if err != nil {
		return gitCommitIdentity{}, fmt.Errorf("cannot verify GitHub PAT owner: %w", err)
	}
	user := gitObj(response)
	login := asString(user["login"])
	id := asInt(user["id"])
	if !githubAccountLogin.MatchString(login) || id <= 0 {
		return gitCommitIdentity{}, errors.New("GitHub PAT did not resolve to a valid user account")
	}
	if accountType := asString(user["type"]); accountType != "" && accountType != "User" {
		return gitCommitIdentity{}, errors.New("GitHub PAT belongs to a non-user account")
	}
	name := strings.TrimSpace(asString(user["name"]))
	if name == "" {
		name = login
	}
	if len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return gitCommitIdentity{}, errors.New("invalid GitHub profile commit name")
	}
	return gitCommitIdentity{
		Login: login,
		Name:  name,
		Email: fmt.Sprintf("%d+%s@users.noreply.github.com", id, login),
	}, nil
}

// PATReadiness verifies authenticated user identity and reported repository
// permissions. It does not perform mutations or claim that CI/merges will pass.
func (g *GitHub) PATReadiness(repositories []string) (map[string]any, error) {
	if len(repositories) > 64 {
		return nil, errors.New("too many repositories for a credential check")
	}
	if g.Token == "" {
		return map[string]any{
			"pat_configured":   false,
			"ready":            false,
			"repository_count": len(repositories),
		}, nil
	}
	identity, err := g.commitIdentity()
	if err != nil {
		return nil, err
	}
	ready := len(repositories) > 0
	checked := make([]map[string]any, 0, len(repositories))
	for _, repository := range repositories {
		prefix, err := repoPath(repository)
		if err != nil {
			return nil, err
		}
		info, err := g.request("GET", prefix, nil, nil)
		result := map[string]any{"repository": repository, "accessible": false,
			"permission_information_available": false, "reported_push": false, "reported_admin": false}
		if err != nil {
			ready = false
			checked = append(checked, result)
			continue
		}
		details := gitObj(info)
		if !strings.EqualFold(asString(details["full_name"]), repository) {
			ready = false
			checked = append(checked, result)
			continue
		}
		result["accessible"] = true
		p, ok := details["permissions"].(map[string]any)
		if ok {
			result["permission_information_available"] = true
			result["reported_push"] = p["push"] == true || p["admin"] == true
			result["reported_admin"] = p["admin"] == true
		}
		if result["reported_push"] != true {
			ready = false
		}
		checked = append(checked, result)
	}
	return map[string]any{
		"pat_configured": true, "authenticated_login": identity.Login,
		"commit_author_name": identity.Name, "commit_author_email": identity.Email,
		"repository_count": len(repositories), "ready": ready, "repositories": checked,
		"note": "Reported account permissions are not a substitute for provider-confirmed publication or administration acceptance tests",
	}, nil
}
