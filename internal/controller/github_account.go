package controller

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AccountRepositories enumerates one bounded page of repositories visible to
// the connected GitHub identity; access does not imply permission to modify.
func (g *GitHub) AccountRepositories(page int) (any, error) {
	if page < 1 || page > 100 {
		return nil, errors.New("invalid repository page")
	}
	return g.request("GET", "/user/repos", map[string]any{
		"affiliation": "owner,collaborator,organization_member",
		"per_page":    100, "page": page, "sort": "full_name",
	}, nil)
}

// SearchGitHubRepositories searches repositories visible to this credential.
func (g *GitHub) SearchGitHubRepositories(query string, page int) (map[string]any, error) {
	if strings.TrimSpace(query) == "" || len(query) > 160 || strings.ContainsAny(query, "\x00\r\n") ||
		page < 1 || page > 100 {
		return nil, errors.New("invalid repository search")
	}
	value, err := g.request("GET", "/search/repositories", map[string]any{
		"q": query, "per_page": 30, "page": page,
	}, nil)
	if err != nil {
		return nil, err
	}
	result := gitObj(value)
	items, ok := result["items"].([]any)
	if !ok {
		return nil, errors.New("GitHub returned an invalid repository search")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		repo := gitObj(item)
		if asString(repo["full_name"]) == "" || asFloat(repo["id"]) <= 0 {
			return nil, errors.New("GitHub returned an invalid repository identity")
		}
		rows = append(rows, map[string]any{"id": repo["id"], "full_name": repo["full_name"],
			"private": repo["private"], "description": repo["description"]})
	}
	return map[string]any{"query": query, "total_count": result["total_count"],
		"repositories": rows, "page": page}, nil
}

func (g *GitHub) AccountRepository(repo string) (map[string]any, error) {
	p, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	value, err := g.request("GET", p, nil, nil)
	if err != nil {
		return nil, err
	}
	record := gitObj(value)
	if asString(record["full_name"]) != repo || asFloat(record["id"]) <= 0 {
		return nil, errors.New("GitHub returned a different repository")
	}
	return record, nil
}

var githubRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var githubAccountName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// accountRepositoryDestination validates the provider locator before a write
// or a reconciliation preflight is attempted.
func accountRepositoryDestination(repo, operation, destination string) (string, error) {
	if _, err := repoPath(repo); err != nil {
		return "", err
	}
	parts := strings.Split(repo, "/")
	switch operation {
	case "rename":
		if !githubRepositoryName.MatchString(destination) || destination == parts[1] {
			return "", errors.New("invalid replacement repository name")
		}
		return parts[0] + "/" + destination, nil
	case "transfer":
		if !githubAccountName.MatchString(destination) || destination == parts[0] {
			return "", errors.New("invalid destination account")
		}
		return destination + "/" + parts[1], nil
	default:
		return "", errors.New("invalid repository operation")
	}
}

// AccountRepositoryChange requires exact provider identity, provider-confirmed
// administration permission, and operation-specific confirmation. Transfers
// may require asynchronous acceptance by the destination organization.
func (g *GitHub) AccountRepositoryChange(repo, operation, destination string, expectedID int64, confirm bool) (map[string]any, error) {
	if !confirm || expectedID <= 0 {
		return nil, errors.New("explicit confirmation and repository ID required")
	}
	path, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(repo, "/")
	next := ""
	method := ""
	body := map[string]any{}
	switch operation {
	case "rename":
		if !githubRepositoryName.MatchString(destination) || destination == parts[1] {
			return nil, errors.New("invalid replacement repository name")
		}
		next, method = parts[0]+"/"+destination, "PATCH"
		body["name"] = destination
	case "transfer":
		if !githubAccountName.MatchString(destination) || destination == parts[0] {
			return nil, errors.New("invalid destination account")
		}
		next, method = destination+"/"+parts[1], "POST"
		path += "/transfer"
		body["new_owner"] = destination
	default:
		return nil, errors.New("invalid repository operation")
	}

	current, err := g.AccountRepository(repo)
	if err != nil {
		return nil, err
	}
	if int64(asFloat(current["id"])) != expectedID ||
		!asBool(gitObj(current["permissions"])["admin"]) {
		return nil, errors.New("repository identity conflict or administrator permission missing")
	}
	modified, err := g.request(method, path, nil, body)
	if err != nil {
		return nil, err
	}
	provider := gitObj(modified)
	if int64(asFloat(provider["id"])) != expectedID {
		return nil, errors.New("GitHub returned an unexpected repository identity")
	}
	// GitHub transfers can be pending destination acceptance; only a read
	// of the destination with the same immutable repository ID proves completion.
	if operation == "rename" && asString(provider["full_name"]) != next {
		return nil, fmt.Errorf("GitHub did not confirm %s destination; inspect provider state", operation)
	}
	result := map[string]any{
		"operation": operation, "before": repo, "after": next,
		"repository_id": expectedID, "confirmed": false, "status": "pending_verification",
	}
	verified, verifyErr := g.AccountRepository(next)
	if verifyErr == nil && int64(asFloat(verified["id"])) == expectedID {
		result["confirmed"], result["status"] = true, "verified"
	}
	return result, nil
}

var accountRepositoryBooleanSettings = map[string]bool{
	"has_issues": true, "has_projects": true, "has_wiki": true,
	"allow_squash_merge": true, "allow_merge_commit": true,
	"allow_rebase_merge": true, "allow_auto_merge": true,
	"delete_branch_on_merge": true, "allow_update_branch": true,
	"web_commit_signoff_required": true, "allow_forking": true,
}

func accountRepositorySettingsView(record map[string]any) map[string]any {
	out := map[string]any{
		"id": record["id"], "full_name": record["full_name"],
		"archived": record["archived"], "visibility": record["visibility"],
		"default_branch": record["default_branch"],
		"description":    record["description"], "homepage": record["homepage"],
	}
	for name := range accountRepositoryBooleanSettings {
		if value, exists := record[name]; exists {
			out[name] = value
		}
	}
	return out
}

// AccountRepositorySettings exposes supported repository options without
// requiring the repository to be linked to a project.
func (g *GitHub) AccountRepositorySettings(repo string) (map[string]any, error) {
	record, err := g.AccountRepository(repo)
	if err != nil {
		return nil, err
	}
	return accountRepositorySettingsView(record), nil
}

// AccountRepositoryConfigure changes only explicitly supported options.
// The expected repository ID protects against a stale or redirected name.
func (g *GitHub) AccountRepositoryConfigure(repo string, expectedID int64, settings map[string]any, confirm bool) (map[string]any, error) {
	if !confirm || expectedID <= 0 || len(settings) == 0 || len(settings) > 16 {
		return nil, errors.New("explicit confirmation, repository ID, and supported settings required")
	}
	body := make(map[string]any, len(settings))
	for key, value := range settings {
		switch {
		case accountRepositoryBooleanSettings[key]:
			enabled, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("repository setting %s requires a boolean", key)
			}
			body[key] = enabled
		case key == "description":
			text, ok := value.(string)
			if !ok || len(text) > 350 || strings.ContainsAny(text, "\x00\r\n") {
				return nil, errors.New("invalid repository description")
			}
			body[key] = text
		case key == "homepage":
			text, ok := value.(string)
			if !ok || len(text) > 2048 || strings.ContainsAny(text, "\x00\r\n\t ") ||
				(text != "" && !strings.HasPrefix(text, "https://")) {
				return nil, errors.New("homepage must be empty or an HTTPS URL")
			}
			body[key] = text
		default:
			return nil, fmt.Errorf("unsupported repository setting: %s", key)
		}
	}
	current, err := g.AccountRepository(repo)
	if err != nil {
		return nil, err
	}
	if int64(asFloat(current["id"])) != expectedID ||
		!asBool(gitObj(current["permissions"])["admin"]) {
		return nil, errors.New("repository identity conflict or administrator permission missing")
	}
	path, _ := repoPath(repo)
	if _, err := g.request("PATCH", path, nil, body); err != nil {
		return nil, err
	}
	verified, err := g.AccountRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("repository settings updated but verification failed: %w", err)
	}
	if int64(asFloat(verified["id"])) != expectedID {
		return nil, errors.New("repository identity changed during settings verification")
	}
	for key, wanted := range body {
		if verified[key] != wanted {
			return nil, fmt.Errorf("repository setting %s not confirmed by GitHub", key)
		}
	}
	return accountRepositorySettingsView(verified), nil
}

// AccountRepositoryArchive makes the archive state explicit. A second GET
// confirms the provider state rather than trusting a mutation response.
func (g *GitHub) AccountRepositoryArchive(repo string, expectedID int64, archived, confirm bool) (map[string]any, error) {
	if !confirm || expectedID <= 0 {
		return nil, errors.New("explicit confirmation and repository ID required")
	}
	current, err := g.AccountRepository(repo)
	if err != nil {
		return nil, err
	}
	if int64(asFloat(current["id"])) != expectedID ||
		!asBool(gitObj(current["permissions"])["admin"]) {
		return nil, errors.New("repository identity conflict or administrator permission missing")
	}
	if current["archived"] == archived {
		return map[string]any{"repository": repo, "repository_id": expectedID, "archived": archived, "verified": true}, nil
	}
	path, _ := repoPath(repo)
	if _, err := g.request("PATCH", path, nil, map[string]any{"archived": archived}); err != nil {
		return nil, err
	}
	verified, err := g.AccountRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("archive state changed but verification failed: %w", err)
	}
	if int64(asFloat(verified["id"])) != expectedID || verified["archived"] != archived {
		return nil, errors.New("GitHub did not confirm the requested archive state")
	}
	return map[string]any{"repository": repo, "repository_id": expectedID, "archived": archived, "verified": true}, nil
}
