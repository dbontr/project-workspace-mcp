package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type GitHub struct {
	Token         string
	API           string
	Client        *http.Client
	GenericWrites bool
	App           *GitHubApp
}

func NewGitHub(token string) *GitHub {
	return &GitHub{Token: token, API: "https://api.github.com", Client: &http.Client{Timeout: 35 * time.Second}, GenericWrites: false}
}
func (g *GitHub) request(method, path string, query map[string]any, body any) (any, error) {
	base, e := url.Parse(g.API)
	if e != nil {
		return nil, e
	}
	target, e := base.Parse(path)
	if e != nil {
		return nil, e
	}
	if base.Host != target.Host {
		return nil, errors.New("GitHub host changed")
	}
	token, e := g.credential(path)
	if e != nil {
		return nil, e
	}
	values := target.Query()
	for key, val := range query {
		values.Set(key, fmt.Sprint(val))
	}
	target.RawQuery = values.Encode()
	var payload io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		payload = bytes.NewReader(raw)
	}
	req, e := http.NewRequest(method, target.String(), payload)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "workspace-mcp")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Reject redirects at the transport boundary so credentials cannot be forwarded.
	client := *g.Client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 5<<20))
	if e != nil {
		return nil, e
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub HTTP %d: %s", res.StatusCode, string(data[:min(len(data), 400)]))
	}
	if len(data) == 0 {
		return nil, nil
	}
	var result any
	if e := json.Unmarshal(data, &result); e != nil {
		return nil, e
	}
	return result, nil
}
func gitObj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func repoPath(repo string) (string, error) {
	if !validGitRepository(repo) {
		return "", errors.New("invalid repository")
	}
	return "/repos/" + repo, nil
}
func (g *GitHub) Repo(repo string) (any, error) {
	path, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	data, e := g.request("GET", path, nil, nil)
	if e != nil {
		return nil, e
	}
	m := gitObj(data)
	return map[string]any{"id": m["id"], "full_name": m["full_name"], "default_branch": m["default_branch"], "private": m["private"]}, nil
}
func (g *GitHub) Commit(repo, ref string) (string, error) {
	path, e := repoPath(repo)
	if e != nil {
		return "", e
	}
	if !regexRef(ref) {
		return "", errors.New("invalid Git ref")
	}
	v, e := g.request("GET", path+"/commits/"+url.PathEscape(ref), nil, nil)
	if e != nil {
		return "", e
	}
	sha := asString(gitObj(v)["sha"])
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(sha) {
		return "", errors.New("GitHub returned invalid revision")
	}
	return sha, nil
}
func validFilePath(path string) bool {
	if !validRelative.MatchString(path) || strings.ContainsAny(path, "\x00\r\n") || len(path) > 1024 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	return true
}
func (g *GitHub) File(repo, path, ref string) (any, error) {
	prefix, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	if !validFilePath(path) {
		return nil, errors.New("invalid repository path")
	}
	escaped := []string{}
	for _, x := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(x))
	}
	res, e := g.request("GET", prefix+"/contents/"+strings.Join(escaped, "/"), map[string]any{"ref": ref}, nil)
	if e != nil {
		return nil, e
	}
	m := gitObj(res)
	if m["type"] != "file" || m["encoding"] != "base64" {
		return nil, errors.New("GitHub file encoding unsupported")
	}
	data, e := base64.StdEncoding.DecodeString(strings.ReplaceAll(asString(m["content"]), "\n", ""))
	if e != nil || len(data) > 131072 {
		return nil, errors.New("invalid or oversized GitHub file")
	}
	return map[string]any{"repo": repo, "path": path, "sha": m["sha"], "revision": ref, "content": string(data)}, nil
}
func (g *GitHub) Issues(repo, state string, pulls bool) (any, error) {
	path, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, errors.New("invalid state")
	}
	target := "issues"
	if pulls {
		target = "pulls"
	}
	r, e := g.request("GET", path+"/"+target, map[string]any{"state": state, "per_page": 50}, nil)
	if e != nil {
		return nil, e
	}
	list, _ := r.([]any)
	out := []map[string]any{}
	for _, v := range list {
		m := gitObj(v)
		if !pulls && m["pull_request"] != nil {
			continue
		}
		entry := map[string]any{"number": m["number"], "title": m["title"], "state": m["state"], "url": m["html_url"]}
		if pulls {
			entry["draft"] = m["draft"]
		}
		out = append(out, entry)
	}
	return out, nil
}
func (g *GitHub) IssueCreate(repo, title, body string) (any, error) {
	path, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(title) == "" || len(title) > 256 || len(body) > 20000 {
		return nil, errors.New("invalid issue")
	}
	v, e := g.request("POST", path+"/issues", nil, map[string]any{"title": title, "body": body})
	if e != nil {
		return nil, e
	}
	m := gitObj(v)
	return map[string]any{"number": m["number"], "url": m["html_url"]}, nil
}
func (g *GitHub) RepositoryAPI(repo, method, endpoint string, query map[string]any, body any) (any, error) {
	prefix, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
		return nil, errors.New("invalid GitHub HTTP method")
	}
	if len(endpoint) > 512 || !regexp.MustCompile(`^[A-Za-z0-9_.@+-]+(/[A-Za-z0-9_.@+-]+)*$`).MatchString(endpoint) || strings.Contains(endpoint, "..") {
		return nil, errors.New("invalid GitHub API endpoint")
	}
	if len(query) > 16 {
		return nil, errors.New("too many query parameters")
	}
	for k, v := range query {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`).MatchString(k) || len(fmt.Sprint(v)) > 1024 {
			return nil, errors.New("invalid query parameter")
		}
	}
	if len(encode(body)) > 256000 {
		return nil, errors.New("GitHub payload too large")
	}
	if method == "GET" && body != nil {
		return nil, errors.New("GET body not allowed")
	}
	if method != "GET" && !g.GenericWrites {
		return nil, errors.New("generic GitHub mutations disabled")
	}
	return g.request(method, prefix+"/"+endpoint, query, body)
}
func (g *GitHub) Snapshot(repo, ref string) (string, []byte, error) {
	prefix, e := repoPath(repo)
	if e != nil {
		return "", nil, e
	}
	sha, e := g.Commit(repo, ref)
	if e != nil {
		return "", nil, e
	}
	target := g.API + prefix + "/tarball/" + sha
	token, e := g.credential(prefix + "/tarball/" + sha)
	if e != nil {
		return "", nil, e
	}
	req, e := http.NewRequest("GET", target, nil)
	if e != nil {
		return "", nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "workspace-mcp")
	noRedirect := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	first, e := noRedirect.Do(req)
	if e != nil {
		return "", nil, e
	}
	first.Body.Close()
	if first.StatusCode < 300 || first.StatusCode > 399 {
		return "", nil, errors.New("GitHub tarball redirect unavailable")
	}
	location, e := url.Parse(first.Header.Get("Location"))
	if e != nil || location.Scheme != "https" || (location.Hostname() != "codeload.github.com" && location.Hostname() != "api.github.com") || location.User != nil {
		return "", nil, errors.New("untrusted archive destination")
	}
	rawReq, e := http.NewRequest("GET", location.String(), nil)
	if e != nil {
		return "", nil, e
	}
	rawReq.Header.Set("User-Agent", "workspace-mcp")
	res, e := noRedirect.Do(rawReq)
	if e != nil {
		return "", nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", nil, fmt.Errorf("archive returned HTTP %d", res.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 12<<20+1))
	if e != nil {
		return "", nil, e
	}
	if len(data) > 12<<20 {
		return "", nil, errors.New("GitHub archive larger than 12 MiB")
	}
	return sha, data, nil
}
func (g *GitHub) Publish(repo, branch, expected string, files map[string][]byte, title, body string) (any, error) {
	prefix, e := repoPath(repo)
	if e != nil {
		return nil, e
	}
	if !regexRef(branch) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(expected) || len(files) < 1 || len(files) > 16 || strings.TrimSpace(title) == "" || len(title) > 256 {
		return nil, errors.New("invalid publication request")
	}
	base, e := g.request("GET", prefix+"/git/ref/heads/"+branch, nil, nil)
	if e != nil {
		return nil, e
	}
	actual := asString(gitObj(gitObj(base)["object"])["sha"])
	if actual != expected {
		return nil, errors.New("GitHub base revision conflict")
	}
	commit, e := g.request("GET", prefix+"/git/commits/"+expected, nil, nil)
	if e != nil {
		return nil, e
	}
	tree := asString(gitObj(gitObj(commit)["tree"])["sha"])
	identity, e := g.commitIdentity()
	if e != nil {
		return nil, e
	}
	entries := []map[string]any{}
	for path, data := range files {
		if !validFilePath(path) || len(data) > 1<<20 {
			return nil, errors.New("invalid changed file")
		}
		blob, e := g.request("POST", prefix+"/git/blobs", nil, map[string]any{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"})
		if e != nil {
			return nil, e
		}
		entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "sha": gitObj(blob)["sha"]})
	}
	next, e := g.request("POST", prefix+"/git/trees", nil, map[string]any{"base_tree": tree, "tree": entries})
	if e != nil {
		return nil, e
	}
	newCommit, e := g.request("POST", prefix+"/git/commits", nil, map[string]any{
		"message": title, "tree": gitObj(next)["sha"], "parents": []string{expected},
		"author":    map[string]string{"name": identity.Name, "email": identity.Email},
		"committer": map[string]string{"name": identity.Name, "email": identity.Email},
	})
	if e != nil {
		return nil, e
	}
	sha := asString(gitObj(newCommit)["sha"])
	if !commitID.MatchString(sha) {
		return nil, errors.New("GitHub returned an invalid publication commit")
	}
	for _, role := range []string{"author", "committer"} {
		person := gitObj(gitObj(newCommit)[role])
		if person["name"] != identity.Name || person["email"] != identity.Email {
			return nil, errors.New("GitHub did not confirm the authenticated commit identity")
		}
	}
	newBranch := "pwm/" + randomHex(8)
	_, e = g.request("POST", prefix+"/git/refs", nil, map[string]any{"ref": "refs/heads/" + newBranch, "sha": sha})
	if e != nil {
		return nil, e
	}
	pr, e := g.request("POST", prefix+"/pulls", nil, map[string]any{"title": title, "head": newBranch, "base": branch, "body": body, "draft": true})
	if e != nil {
		return nil, e
	}
	m := gitObj(pr)
	return map[string]any{"url": m["html_url"], "number": m["number"], "branch": newBranch, "commit_sha": sha, "base_sha": expected, "draft": true, "author_login": identity.Login}, nil
}
