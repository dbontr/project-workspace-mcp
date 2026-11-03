package controller

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validateBranch rejects ambiguous Git refs before interpolating them into API routes.
func validateBranch(branch string) bool {
	if !regexRef(branch) || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "//") || strings.Contains(branch, "/.") ||
		strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "\\") {
		return false
	}
	return true
}

func validNumber(number int) bool { return number > 0 && number <= 100_000_000 }
func pullPath(repo string, number int) (string, error) {
	path, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	if !validNumber(number) {
		return "", errors.New("invalid pull request number")
	}
	return path + "/pulls/" + strconv.Itoa(number), nil
}

func (g *GitHub) Issue(repo string, number int) (map[string]any, error) {
	path, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !validNumber(number) {
		return nil, errors.New("invalid issue number")
	}
	result, err := g.request("GET", path+"/issues/"+strconv.Itoa(number), nil, nil)
	if err != nil {
		return nil, err
	}
	record := gitObj(result)
	if record["pull_request"] != nil {
		return nil, errors.New("number identifies a pull request, not an issue")
	}
	return record, nil
}

// IssueUpdate performs a provider-state comparison before changing an issue.
func (g *GitHub) IssueUpdate(repo string, number int, expectedState, state, stateReason string, title, body *string) (any, error) {
	if expectedState != "open" && expectedState != "closed" {
		return nil, errors.New("expected issue state required")
	}
	if state != "" && state != "open" && state != "closed" {
		return nil, errors.New("invalid issue state")
	}
	if stateReason != "" && (state != "closed" || (stateReason != "completed" && stateReason != "not_planned")) {
		return nil, errors.New("invalid issue closure reason")
	}
	if title != nil && (strings.TrimSpace(*title) == "" || len(*title) > 256) {
		return nil, errors.New("invalid issue title")
	}
	if body != nil && len(*body) > 20_000 {
		return nil, errors.New("issue body too large")
	}
	if state == "" && title == nil && body == nil {
		return nil, errors.New("no issue changes requested")
	}
	found, err := g.Issue(repo, number)
	if err != nil {
		return nil, err
	}
	if asString(found["state"]) != expectedState {
		return nil, errors.New("issue state conflict")
	}
	prefix, _ := repoPath(repo)
	change := map[string]any{}
	if state != "" {
		change["state"] = state
	}
	if stateReason != "" {
		change["state_reason"] = stateReason
	}
	if title != nil {
		change["title"] = *title
	}
	if body != nil {
		change["body"] = *body
	}
	return g.request("PATCH", prefix+"/issues/"+strconv.Itoa(number), nil, change)
}

func (g *GitHub) IssueComment(repo string, number int, body string) (any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !validNumber(number) || strings.TrimSpace(body) == "" || len(body) > 20_000 {
		return nil, errors.New("invalid issue comment")
	}
	// GitHub accepts comments on both issues and pull requests.
	return g.request("POST", prefix+"/issues/"+strconv.Itoa(number)+"/comments", nil, map[string]any{"body": body})
}

func (g *GitHub) Branches(repo string) ([]map[string]any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for page := 1; page <= 10; page++ {
		data, err := g.request("GET", prefix+"/branches", map[string]any{"per_page": 100, "page": page}, nil)
		if err != nil {
			return nil, err
		}
		rows, ok := data.([]any)
		if !ok {
			return nil, errors.New("invalid branch listing")
		}
		for _, row := range rows {
			branch := gitObj(row)
			out = append(out, map[string]any{"name": branch["name"], "protected": branch["protected"], "sha": gitObj(branch["commit"])["sha"]})
		}
		if len(rows) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("too many branches to list safely")
}

func (g *GitHub) Branch(repo, branch string) (map[string]any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !validateBranch(branch) {
		return nil, errors.New("invalid branch")
	}
	data, err := g.request("GET", prefix+"/branches/"+branch, nil, nil)
	if err != nil {
		return nil, err
	}
	info := gitObj(data)
	return map[string]any{"name": info["name"], "protected": info["protected"], "sha": gitObj(info["commit"])["sha"]}, nil
}

// BranchCreate generates only names under the managed prefix. Unmanaged branches
// can be linked to projects but are not created implicitly by the agent.
func (g *GitHub) BranchCreate(repo, branch, fromRef string) (any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(branch, "pwm/") || !validateBranch(branch) || branch == "pwm/" || !validateBranch(fromRef) {
		return nil, errors.New("a valid pwm/ branch and base ref are required")
	}
	sha, err := g.Commit(repo, fromRef)
	if err != nil {
		return nil, err
	}
	result, err := g.request("POST", prefix+"/git/refs", nil, map[string]any{"ref": "refs/heads/" + branch, "sha": sha})
	if err != nil {
		return nil, err
	}
	return map[string]any{"branch": branch, "from_sha": sha, "ref": gitObj(result)["ref"]}, nil
}

func (g *GitHub) Pull(repo string, number int) (map[string]any, error) {
	path, err := pullPath(repo, number)
	if err != nil {
		return nil, err
	}
	result, err := g.request("GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	data := gitObj(result)
	if !validNumber(asInt(data["number"])) {
		return nil, errors.New("GitHub returned invalid pull request")
	}
	return data, nil
}

func (g *GitHub) PullCreate(repo, base, head, title, body string, draft bool) (any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !validateBranch(base) || !validateBranch(head) || !strings.HasPrefix(head, "pwm/") ||
		head == base || strings.TrimSpace(title) == "" || len(title) > 256 || len(body) > 20_000 {
		return nil, errors.New("invalid pull request creation")
	}
	result, err := g.request("POST", prefix+"/pulls", nil, map[string]any{
		"base": base, "head": head, "title": title, "body": body, "draft": draft,
	})
	if err != nil {
		return nil, err
	}
	data := gitObj(result)
	return map[string]any{"number": data["number"], "url": data["html_url"], "head_sha": gitObj(data["head"])["sha"], "draft": data["draft"]}, nil
}

func (g *GitHub) PullUpdate(repo string, number int, expectedHead string, title, body *string) (any, error) {
	if !commitID.MatchString(expectedHead) || (title == nil && body == nil) {
		return nil, errors.New("expected head and pull request changes required")
	}
	if title != nil && (strings.TrimSpace(*title) == "" || len(*title) > 256) {
		return nil, errors.New("invalid pull request title")
	}
	if body != nil && len(*body) > 20_000 {
		return nil, errors.New("pull request body too large")
	}
	current, err := g.Pull(repo, number)
	if err != nil {
		return nil, err
	}
	if asString(gitObj(current["head"])["sha"]) != expectedHead || current["state"] != "open" {
		return nil, errors.New("pull request revision conflict")
	}
	path, _ := pullPath(repo, number)
	change := map[string]any{}
	if title != nil {
		change["title"] = *title
	}
	if body != nil {
		change["body"] = *body
	}
	return g.request("PATCH", path, nil, change)
}

func (g *GitHub) PullReady(repo string, number int, expectedHead string) (any, error) {
	if !commitID.MatchString(expectedHead) {
		return nil, errors.New("expected pull request head SHA required")
	}
	pr, err := g.Pull(repo, number)
	if err != nil {
		return nil, err
	}
	if pr["state"] != "open" || asString(gitObj(pr["head"])["sha"]) != expectedHead {
		return nil, errors.New("pull request revision conflict")
	}
	if pr["draft"] != true {
		return map[string]any{"number": number, "draft": false}, nil
	}
	id := asString(pr["node_id"])
	if !validGraphID.MatchString(id) {
		return nil, errors.New("GitHub pull request node ID unavailable")
	}
	data, err := g.GraphQL(`mutation($id: ID!) { markPullRequestReadyForReview(input:{pullRequestId:$id}) { pullRequest { id isDraft } } }`, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	target := gitObj(gitObj(data["markPullRequestReadyForReview"])["pullRequest"])
	if target["isDraft"] != false {
		return nil, errors.New("GitHub did not confirm pull request readiness")
	}
	return map[string]any{"number": number, "draft": false}, nil
}

// PullVerification is a provider-derived quality gate; branch protections and
// required reviewer approvals are ultimately enforced by GitHub's merge API.
func (g *GitHub) PullVerification(repo string, number int) (map[string]any, error) {
	pr, err := g.Pull(repo, number)
	if err != nil {
		return nil, err
	}
	head := asString(gitObj(pr["head"])["sha"])
	if !commitID.MatchString(head) {
		return nil, errors.New("GitHub returned invalid head revision")
	}
	prefix, _ := repoPath(repo)
	checksRaw, err := g.request("GET", prefix+"/commits/"+head+"/check-runs", map[string]any{"per_page": 100}, nil)
	if err != nil {
		return nil, err
	}
	checks := gitObj(checksRaw)
	total := asInt(checks["total_count"])
	checkItems, ok := checks["check_runs"].([]any)
	if !ok || total < len(checkItems) {
		return nil, errors.New("GitHub check results unavailable")
	}
	checksPass := total > 0 && total <= 100 && len(checkItems) == total
	failed := []string{}
	for _, item := range checkItems {
		c := gitObj(item)
		status, conclusion := asString(c["status"]), asString(c["conclusion"])
		if status != "completed" || (conclusion != "success" && conclusion != "neutral" && conclusion != "skipped") {
			checksPass = false
			failed = append(failed, asString(c["name"]))
		}
	}
	statusesRaw, err := g.request("GET", prefix+"/commits/"+head+"/status", nil, nil)
	if err != nil {
		return nil, err
	}
	status := gitObj(statusesRaw)
	if n := asInt(status["total_count"]); n > 0 && status["state"] != "success" {
		checksPass = false
	}
	mergeable := pr["mergeable"] == true && asString(pr["mergeable_state"]) == "clean"
	ready := pr["state"] == "open" && pr["draft"] != true && pr["merged"] != true && mergeable && checksPass
	return map[string]any{
		"number": number, "url": pr["html_url"], "state": pr["state"], "draft": pr["draft"],
		"head_sha": head, "base_sha": gitObj(pr["base"])["sha"], "branch": gitObj(pr["head"])["ref"],
		"mergeable": mergeable, "mergeable_state": pr["mergeable_state"],
		"check_count": total, "checks_passed": checksPass, "incomplete_or_failing_checks": failed,
		"ready_to_merge": ready,
	}, nil
}

func (g *GitHub) PullMerge(repo string, number int, expectedHead, method string, confirm, cleanup bool) (map[string]any, error) {
	if !confirm || !commitID.MatchString(expectedHead) {
		return nil, errors.New("explicit merge confirmation and expected head SHA required")
	}
	if method == "" {
		method = "squash"
	}
	if method != "squash" && method != "merge" && method != "rebase" {
		return nil, errors.New("invalid merge method")
	}
	verified, err := g.PullVerification(repo, number)
	if err != nil {
		return nil, err
	}
	if verified["head_sha"] != expectedHead || verified["ready_to_merge"] != true {
		return nil, errors.New("pull request verification or revision conflict")
	}
	path, _ := pullPath(repo, number)
	result, err := g.request("PUT", path+"/merge", nil, map[string]any{"sha": expectedHead, "merge_method": method})
	if err != nil {
		return nil, err
	}
	outcome := gitObj(result)
	if outcome["merged"] != true || !commitID.MatchString(asString(outcome["sha"])) {
		return nil, errors.New("GitHub did not confirm pull request merge")
	}
	after, err := g.Pull(repo, number)
	if err != nil {
		return nil, fmt.Errorf("merge accepted but verification failed: %w", err)
	}
	if after["merged"] != true || asString(gitObj(after["head"])["sha"]) != expectedHead {
		return nil, errors.New("GitHub merge result did not match the expected pull request")
	}
	response := map[string]any{"number": number, "merged": true, "merge_sha": outcome["sha"], "url": after["html_url"]}
	if cleanup {
		branch := asString(gitObj(after["head"])["ref"])
		if !strings.HasPrefix(branch, "pwm/") {
			response["cleanup"] = map[string]any{"deleted": false, "reason": "only agent-managed branches are deleted automatically"}
		} else {
			result, e := g.CleanupMergedBranch(repo, number, expectedHead, true)
			if e != nil {
				response["cleanup"] = map[string]any{"deleted": false, "error": e.Error()}
			} else {
				response["cleanup"] = result
			}
		}
	}
	return response, nil
}

func (g *GitHub) CleanupMergedBranch(repo string, number int, expectedHead string, confirm bool) (map[string]any, error) {
	if !confirm || !commitID.MatchString(expectedHead) {
		return nil, errors.New("explicit branch cleanup confirmation and expected SHA required")
	}
	pr, err := g.Pull(repo, number)
	if err != nil {
		return nil, err
	}
	if pr["merged"] != true || pr["state"] != "closed" {
		return nil, errors.New("pull request must be merged before branch cleanup")
	}
	head := gitObj(pr["head"])
	name := asString(head["ref"])
	if !validateBranch(name) || asString(head["sha"]) != expectedHead {
		return nil, errors.New("merged branch revision conflict")
	}
	if !strings.EqualFold(asString(gitObj(head["repo"])["full_name"]), repo) {
		return map[string]any{"deleted": false, "reason": "fork branch outside linked repository"}, nil
	}
	info, err := g.Repo(repo)
	if err != nil {
		return nil, err
	}
	defaultBranch := asString(gitObj(info)["default_branch"])
	if name == defaultBranch || name == "main" || name == "master" {
		return map[string]any{"deleted": false, "reason": "default or reserved branch"}, nil
	}
	prefix, _ := repoPath(repo)
	// Do not delete a branch used as the head or base of any open PR.
	for page := 1; page <= 10; page++ {
		raw, e := g.request("GET", prefix+"/pulls", map[string]any{"state": "open", "per_page": 100, "page": page}, nil)
		if e != nil {
			return nil, e
		}
		prs, ok := raw.([]any)
		if !ok {
			return nil, errors.New("could not inspect open pull requests")
		}
		for _, item := range prs {
			cur := gitObj(item)
			h, b := gitObj(cur["head"]), gitObj(cur["base"])
			if (asString(h["ref"]) == name && strings.EqualFold(asString(gitObj(h["repo"])["full_name"]), repo)) ||
				(asString(b["ref"]) == name && strings.EqualFold(asString(gitObj(b["repo"])["full_name"]), repo)) {
				return map[string]any{"deleted": false, "reason": "branch is referenced by an open pull request"}, nil
			}
		}
		if len(prs) < 100 {
			break
		}
		if page == 10 {
			return nil, errors.New("open pull request listing exceeds safety limit")
		}
	}
	branch, err := g.Branch(repo, name)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return map[string]any{"deleted": false, "reason": "branch already deleted"}, nil
		}
		return nil, err
	}
	if branch["protected"] != false {
		return map[string]any{"deleted": false, "reason": "branch is protected or protection is unknown"}, nil
	}
	if branch["sha"] != expectedHead {
		return nil, errors.New("branch changed since the pull request merge")
	}
	// GitHub's delete-ref endpoint lacks conditional ref deletion. Recheck
	// immediately before deletion; repository rules remain the final authority.
	ref, err := g.request("GET", prefix+"/git/ref/heads/"+name, nil, nil)
	if err != nil {
		return nil, err
	}
	if asString(gitObj(gitObj(ref)["object"])["sha"]) != expectedHead {
		return nil, errors.New("branch moved before deletion")
	}
	if _, err := g.request("DELETE", prefix+"/git/refs/heads/"+name, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "branch": name, "revision": expectedHead}, nil
}

// SetAutoDeleteMergedBranches delegates repository-wide cleanup to GitHub.
// GitHub enforces protection and open-PR rules; repository administration
// permission is required to change this setting.
func (g *GitHub) SetAutoDeleteMergedBranches(repo string, enabled, confirm bool) (map[string]any, error) {
	if !confirm {
		return nil, errors.New("explicit repository setting confirmation required")
	}
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	changed, err := g.request("PATCH", prefix, nil, map[string]any{"delete_branch_on_merge": enabled})
	if err != nil {
		return nil, err
	}
	if gitObj(changed)["delete_branch_on_merge"] != enabled {
		return nil, errors.New("GitHub did not confirm automatic branch deletion setting")
	}
	return map[string]any{"repository": repo, "delete_branch_on_merge": enabled}, nil
}
