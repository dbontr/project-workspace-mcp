package controller

import (
	"errors"
	"fmt"
)

// AppReadiness verifies linked repositories using a live, repository-scoped
// GitHub App installation credential. It never exposes installation tokens or
// the signing key, and it does not perform repository mutations.
func (g *GitHub) AppReadiness(repositories []string) (map[string]any, error) {
	if len(repositories) > 64 {
		return nil, errors.New("too many linked repositories for a readiness check")
	}
	if g.App == nil {
		return map[string]any{
			"app_configured": false, "ready": false,
			"repository_count": len(repositories),
			"detail":           "configure a GitHub App installation and owner-only signing key",
		}, nil
	}
	required := []struct {
		name, access string
	}{
		{"contents", "write"},
		{"issues", "write"},
		{"pull_requests", "write"},
		{"checks", "read"},
		{"statuses", "read"},
	}
	allReady := len(repositories) > 0
	checks := make([]map[string]any, 0, len(repositories))
	for _, repo := range repositories {
		if !validGitRepository(repo) {
			return nil, errors.New("invalid linked GitHub repository")
		}
		item := map[string]any{
			"repository":           repo,
			"read_verified":        false,
			"permissions_verified": false,
			"missing_permissions":  []string{},
		}
		info, err := g.Repo(repo)
		if err != nil || gitObj(info)["full_name"] != repo {
			item["detail"] = "repository unavailable to this installation"
			allReady = false
			checks = append(checks, item)
			continue
		}
		item["read_verified"] = true
		item["default_branch"] = gitObj(info)["default_branch"]
		scope, err := githubTokenScope("/repos/" + repo)
		if err != nil {
			return nil, fmt.Errorf("invalid GitHub installation scope: %w", err)
		}
		g.App.mu.Lock()
		credential, found := g.App.cached[scope]
		permissions := make(map[string]string, len(credential.permissions))
		for key, access := range credential.permissions {
			permissions[key] = access
		}
		g.App.mu.Unlock()
		missing := make([]string, 0, len(required))
		if !found || len(permissions) == 0 {
			for _, permission := range required {
				missing = append(missing, permission.name+":"+permission.access)
			}
			item["detail"] = "GitHub did not provide installation permission metadata"
		} else {
			item["permissions_verified"] = true
			for _, permission := range required {
				actual := permissions[permission.name]
				if actual != "write" && !(permission.access == "read" && actual == "read") {
					missing = append(missing, permission.name+":"+permission.access)
				}
			}
		}
		item["missing_permissions"] = missing
		if len(missing) != 0 {
			allReady = false
		}
		checks = append(checks, item)
	}
	return map[string]any{
		"app_configured":   true,
		"ready":            allReady,
		"repository_count": len(repositories),
		"repositories":     checks,
		"note":             "Read access and declared installation permissions were checked; publication, CI, and merge require separate live acceptance",
	}, nil
}
