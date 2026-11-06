package controller

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// CommitKnowledgeDocument updates only the selected knowledge path. GitHub
// branch protection and a non-forced ref update are both authoritative.
func (g *GitHub) CommitKnowledgeDocument(repo, branch, path, expected, body, message string) (map[string]any, error) {
	prefix, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if !validateBranch(branch) || !commitID.MatchString(expected) ||
		!validFilePath(path) || len(body) == 0 || len(body) > 128<<10 ||
		strings.TrimSpace(message) == "" || len(message) > 256 {
		return nil, errors.New("invalid Git knowledge publication")
	}
	reference, err := g.request("GET", prefix+"/git/ref/heads/"+branch, nil, nil)
	if err != nil {
		return nil, err
	}
	if asString(gitObj(gitObj(reference)["object"])["sha"]) != expected {
		return nil, errors.New("Git knowledge branch revision conflict")
	}
	baseCommit, err := g.request("GET", prefix+"/git/commits/"+expected, nil, nil)
	if err != nil {
		return nil, err
	}
	tree := asString(gitObj(gitObj(baseCommit)["tree"])["sha"])
	if !commitID.MatchString(tree) {
		return nil, errors.New("GitHub returned invalid base tree")
	}
	identity, err := g.commitIdentity()
	if err != nil {
		return nil, err
	}
	blob, err := g.request("POST", prefix+"/git/blobs", nil,
		map[string]any{"content": base64.StdEncoding.EncodeToString([]byte(body)), "encoding": "base64"})
	if err != nil {
		return nil, err
	}
	blobSHA := asString(gitObj(blob)["sha"])
	if !commitID.MatchString(blobSHA) {
		return nil, errors.New("GitHub returned invalid knowledge blob")
	}
	nextTree, err := g.request("POST", prefix+"/git/trees", nil, map[string]any{
		"base_tree": tree,
		"tree":      []map[string]any{{"path": path, "mode": "100644", "type": "blob", "sha": blobSHA}},
	})
	if err != nil {
		return nil, err
	}
	nextSHA := asString(gitObj(nextTree)["sha"])
	if !commitID.MatchString(nextSHA) {
		return nil, errors.New("GitHub returned invalid knowledge tree")
	}
	created, err := g.request("POST", prefix+"/git/commits", nil, map[string]any{
		"message": message, "tree": nextSHA, "parents": []string{expected},
		"author":    map[string]string{"name": identity.Name, "email": identity.Email},
		"committer": map[string]string{"name": identity.Name, "email": identity.Email},
	})
	if err != nil {
		return nil, err
	}
	revision := asString(gitObj(created)["sha"])
	if !commitID.MatchString(revision) {
		return nil, errors.New("GitHub returned invalid knowledge commit")
	}
	_, err = g.request("PATCH", prefix+"/git/refs/heads/"+branch, nil, map[string]any{
		"sha": revision, "force": false,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge branch publication rejected: %w", err)
	}
	remote, err := g.request("GET", prefix+"/git/ref/heads/"+branch, nil, nil)
	if err != nil || asString(gitObj(gitObj(remote)["object"])["sha"]) != revision {
		return nil, errors.New("Git knowledge commit was not confirmed remotely")
	}
	verified, err := g.File(repo, path, revision)
	if err != nil || asString(gitObj(verified)["content"]) != body {
		return nil, errors.New("Git knowledge content verification failed")
	}
	return map[string]any{"repository": repo, "branch": branch,
		"path": path, "sha": revision, "content_sha256": checksum([]byte(body)), "verified": true}, nil
}
