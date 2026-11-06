package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LocalGit operates on operator-mounted bare Git repositories. It does not
// fetch untrusted URLs, execute repository hooks, or pass credentials to workers.
type LocalGit struct {
	Root        string
	AuthorName  string
	AuthorEmail string
}

func localRepoName(value string) (string, bool) {
	if !strings.HasPrefix(value, "local:") {
		return "", false
	}
	name := strings.TrimPrefix(value, "local:")
	return name, validGitRepository(name)
}

func validSourceRepository(value string) bool {
	if _, ok := localRepoName(value); ok {
		return true
	}
	return validGitRepository(value)
}

func validKnowledgeRepository(value string) bool {
	if _, ok := localRepoName(value); ok {
		return true
	}
	return validGitRepository(value)
}

func (g *LocalGit) repository(name string) (string, error) {
	if g == nil || g.Root == "" {
		return "", errors.New("local Git storage is not configured")
	}
	if !validGitRepository(name) {
		return "", errors.New("invalid local Git repository identifier")
	}
	parts := strings.Split(name, "/")
	directory := filepath.Join(g.Root, parts[0], parts[1]+".git")
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("local Git repository is unavailable: %w", err)
	}
	if !strings.HasPrefix(resolved, g.Root+string(filepath.Separator)) {
		return "", errors.New("local Git repository is outside the approved storage root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("local Git repository must be a directory")
	}
	return resolved, nil
}

type gitBoundedWriter struct {
	bytes.Buffer
	Limit int
}

func (w *gitBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.Limit-w.Len() {
		return 0, errors.New("Git command output exceeds the permitted limit")
	}
	return w.Buffer.Write(p)
}

func (g *LocalGit) command(dir, index string, data []byte, maxOutput int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "--git-dir=" + dir}, args...)...)
	if data != nil {
		command.Stdin = bytes.NewReader(data)
	}
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+g.AuthorName,
		"GIT_AUTHOR_EMAIL="+g.AuthorEmail,
		"GIT_COMMITTER_NAME="+g.AuthorName,
		"GIT_COMMITTER_EMAIL="+g.AuthorEmail,
	)
	if index != "" {
		command.Env = append(command.Env, "GIT_INDEX_FILE="+index)
	}
	output := &gitBoundedWriter{Limit: maxOutput}
	stderr := &gitBoundedWriter{Limit: 1024}
	command.Stdout, command.Stderr = output, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("local Git command timed out")
		}
		return nil, fmt.Errorf("local Git operation failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return output.Bytes(), nil
}

func (g *LocalGit) branchHead(name, branch string) (string, error) {
	if !validateBranch(branch) {
		return "", errors.New("invalid Git branch")
	}
	dir, err := g.repository(name)
	if err != nil {
		return "", err
	}
	bare, err := g.command(dir, "", nil, 64, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(string(bare)) != "true" {
		return "", errors.New("local Git repository must be bare")
	}
	raw, err := g.command(dir, "", nil, 64, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(raw))
	if !commitID.MatchString(head) {
		return "", errors.New("invalid local Git branch commit")
	}
	return head, nil
}

func (g *LocalGit) Resolve(name, ref string) (string, error) {
	if !commitID.MatchString(ref) {
		return g.branchHead(name, ref)
	}
	dir, err := g.repository(name)
	if err != nil {
		return "", err
	}
	if _, err := g.command(dir, "", nil, 64, "cat-file", "-e", ref+"^{commit}"); err != nil {
		return "", err
	}
	return ref, nil
}

func (g *LocalGit) ReadAtCommit(name, sha, path string) (map[string]any, error) {
	if !validFilePath(path) || !commitID.MatchString(sha) {
		return nil, errors.New("invalid local Git file revision or path")
	}
	verified, err := g.Resolve(name, sha)
	if err != nil {
		return nil, err
	}
	dir, err := g.repository(name)
	if err != nil {
		return nil, err
	}
	body, err := g.command(dir, "", nil, 128<<10, "show", verified+":"+path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repository": "local:" + name, "provider": "local-git",
		"path": path, "commit_sha": verified, "revision": verified,
		"content": string(body), "content_sha256": checksum(body)}, nil
}

func (g *LocalGit) ReadFile(name, branch, path string) (map[string]any, error) {
	head, err := g.Resolve(name, branch)
	if err != nil {
		return nil, err
	}
	return g.ReadAtCommit(name, head, path)
}

func (g *LocalGit) Publish(name, branch, path, expected, body, message string) (map[string]any, error) {
	if !validFilePath(path) || !commitID.MatchString(expected) ||
		len(body) == 0 || len(body) > 128<<10 || strings.TrimSpace(message) == "" ||
		len(message) > 256 {
		return nil, errors.New("invalid local Git publication request")
	}
	if strings.TrimSpace(g.AuthorName) == "" || strings.TrimSpace(g.AuthorEmail) == "" {
		return nil, errors.New("local Git publication requires an explicitly configured author identity")
	}
	current, err := g.branchHead(name, branch)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, errors.New("local Git publication revision conflict")
	}
	dir, err := g.repository(name)
	if err != nil {
		return nil, err
	}
	blob, err := g.command(dir, "", []byte(body), 64, "hash-object", "-w", "--stdin")
	if err != nil {
		return nil, err
	}
	blobSHA := strings.TrimSpace(string(blob))
	if !commitID.MatchString(blobSHA) {
		return nil, errors.New("invalid local Git blob")
	}
	index, err := os.CreateTemp("", "project-workspace-index-*")
	if err != nil {
		return nil, err
	}
	indexPath := index.Name()
	index.Close()
	os.Remove(indexPath)
	defer os.Remove(indexPath)
	if _, err := g.command(dir, indexPath, nil, 64, "read-tree", expected); err != nil {
		return nil, err
	}
	if _, err := g.command(dir, indexPath, nil, 64, "update-index",
		"--add", "--cacheinfo", "100644,"+blobSHA+","+path); err != nil {
		return nil, err
	}
	tree, err := g.command(dir, indexPath, nil, 64, "write-tree")
	if err != nil {
		return nil, err
	}
	treeSHA := strings.TrimSpace(string(tree))
	if !commitID.MatchString(treeSHA) {
		return nil, errors.New("invalid local Git tree")
	}
	created, err := g.command(dir, "", nil, 64, "commit-tree", treeSHA, "-p", expected, "-m", message)
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(created))
	if !commitID.MatchString(commit) {
		return nil, errors.New("invalid local Git commit")
	}
	if _, err := g.command(dir, "", nil, 64, "update-ref", "refs/heads/"+branch, commit, expected); err != nil {
		return nil, fmt.Errorf("local Git revision conflict: %w", err)
	}
	remote, err := g.ReadFile(name, branch, path)
	if err != nil || remote["commit_sha"] != commit || remote["content"] != body {
		return nil, errors.New("local Git publication could not be verified")
	}
	return map[string]any{"repository": "local:" + name, "provider": "local-git",
		"branch": branch, "path": path, "sha": commit,
		"content_sha256": checksum([]byte(body)), "verified": true}, nil
}

// Archive streams an exact commit as a tar archive, with no local checkout
// and no repository-controlled hooks. A caller must enforce artifact quotas.
func (g *LocalGit) Archive(name, branch string, output io.Writer) (string, error) {
	sha, err := g.Resolve(name, branch)
	if err != nil {
		return "", err
	}
	dir, err := g.repository(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath="+os.DevNull,
		"--git-dir="+dir, "archive", "--format=tar", "--prefix=local-"+sha[:12]+"/", sha)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("local Git archive failed: %w", err)
	}
	return sha, nil
}
