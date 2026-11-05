package controller

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func localGitFixture(t *testing.T) (*LocalGit, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable is not available")
	}
	root := t.TempDir()
	bareDir := filepath.Join(root, "research", "notes.git")
	if err := os.MkdirAll(filepath.Dir(bareDir), 0700); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, root, "init", "--bare", bareDir)
	work := t.TempDir()
	gitForTest(t, work, "init")
	gitForTest(t, work, "config", "user.name", "Test Author")
	gitForTest(t, work, "config", "user.email", "test@example.org")
	if err := os.MkdirAll(filepath.Join(work, "knowledge"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "knowledge", "Context.md"), []byte("# Original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, work, "add", ".")
	gitForTest(t, work, "commit", "-m", "Seed knowledge")
	original := gitForTest(t, work, "rev-parse", "HEAD")
	gitForTest(t, work, "remote", "add", "origin", bareDir)
	gitForTest(t, work, "push", "origin", "HEAD:refs/heads/main")
	return &LocalGit{Root: root, AuthorName: "Test Author", AuthorEmail: "test@example.org"}, "research/notes", original
}

func TestLocalGitKnowledgeRevisionChecksAndImmutableRead(t *testing.T) {
	local, name, original := localGitFixture(t)
	if _, ok := localRepoName("local:" + name); !ok {
		t.Fatal("local Git repository identifier was rejected")
	}
	if validSourceRepository("local:../etc/passwd") {
		t.Fatal("path traversal was accepted as a source repository")
	}
	initial, err := local.ReadFile(name, "main", "knowledge/Context.md")
	if err != nil || initial["content"] != "# Original\n" || initial["commit_sha"] != original {
		t.Fatalf("local Git read: %v %v", initial, err)
	}
	saved, err := local.Publish(name, "main", "knowledge/Context.md", original, "# Verified\n", "Record finding")
	if err != nil || saved["verified"] != true || saved["sha"] == original {
		t.Fatalf("local Git publication failed: %v %v", saved, err)
	}
	if _, err := local.Publish(name, "main", "knowledge/Context.md", original,
		"# Stale\n", "Stale update"); err == nil {
		t.Fatal("outdated head was allowed to overwrite the current branch")
	}
	old, err := local.ReadAtCommit(name, original, "knowledge/Context.md")
	if err != nil || old["content"] != "# Original\n" {
		t.Fatalf("historical Git revision changed: %v %v", old, err)
	}
	current, err := local.ReadFile(name, "main", "knowledge/Context.md")
	if err != nil || current["content"] != "# Verified\n" {
		t.Fatalf("published content not durable: %v %v", current, err)
	}
}

func TestLocalGitArchiveHasSingleCheckoutRoot(t *testing.T) {
	local, name, original := localGitFixture(t)
	reader, writer := io.Pipe()
	go func() {
		zipWriter := gzip.NewWriter(writer)
		_, err := local.Archive(name, original, zipWriter)
		if closeErr := zipWriter.Close(); err == nil {
			err = closeErr
		}
		writer.CloseWithError(err)
	}()
	gz, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	foundFile := false
	for {
		entry, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if entry.Typeflag == tar.TypeXGlobalHeader || entry.Typeflag == tar.TypeXHeader {
			continue
		}
		if !strings.HasPrefix(entry.Name, "local-"+original[:12]+"/") {
			t.Fatalf("archive member escapes checkout root: %q", entry.Name)
		}
		if entry.Typeflag == tar.TypeReg {
			foundFile = true
		}
	}
	if !foundFile {
		t.Fatal("local Git archive contains no source files")
	}
}

func TestLocalGitProjectKnowledgeUsesExplicitProvider(t *testing.T) {
	db, server, httpServer := fixture(t)
	local, name, original := localGitFixture(t)
	server.LocalGit = local
	project(t, httpServer)
	source := "local:" + name
	mcp := func(tool string, args map[string]any) (map[string]any, bool) {
		t.Helper()
		code, response := call(t, httpServer, "POST", "/mcp/", testOwner, map[string]any{
			"jsonrpc": "2.0", "id": 11, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args},
		})
		wantOK(t, code, response)
		result := gitObj(response["result"])
		return gitObj(result["structuredContent"]), result["isError"] == true
	}
	available, failed := mcp("git_provider_capabilities", map[string]any{"provider": "local-git"})
	if failed || available["issues"] != "unavailable" || available["git_storage"] != "supported" {
		t.Fatalf("local Git capabilities misrepresented: %v %v", available, failed)
	}
	_, failed = mcp("git_link_repository", map[string]any{
		"project_id": "demo", "repository": source, "ref": "main", "role": "planning",
	})
	if !failed {
		t.Fatal("local source was accepted as a GitHub planning repository")
	}
	_, failed = mcp("link_github_repository", map[string]any{
		"project_id": "demo", "repository": source, "role": "planning", "default_ref": "main",
	})
	if !failed {
		t.Fatal("GitHub-specific link accepted a local bare Git repository")
	}
	_, failed = mcp("git_link_repository", map[string]any{
		"project_id": "demo", "repository": source, "ref": "main",
	})
	if failed || !db.RepositoryLinked("demo", source) {
		t.Fatal("local Git repository could not be linked")
	}
	_, failed = mcp("knowledge_configure", map[string]any{
		"project_id": "demo", "mode": "git", "repository": source,
		"branch": "main", "prefix": "knowledge", "expected_version": 0,
	})
	if failed {
		t.Fatal("local Git knowledge configuration failed")
	}
	_, failed = mcp("write_project_note", map[string]any{
		"project_id": "demo", "name": "plan", "body": "must not persist", "expected_version": 0,
	})
	if !failed {
		t.Fatal("legacy local note write escaped Git knowledge policy")
	}
	if _, err := db.ReadNote("demo", "plan"); err == nil {
		t.Fatal("legacy note was persisted")
	}
	published, failed := mcp("knowledge_git_publish", map[string]any{
		"project_id": "demo", "path": "Context.md", "expected_head": original,
		"body": "# Accepted\n", "message": "Store findings",
	})
	if failed || published["verified"] != true {
		t.Fatalf("knowledge publication failed: %v %v", published, failed)
	}
	read, failed := mcp("knowledge_git_read", map[string]any{
		"project_id": "demo", "path": "Context.md",
	})
	if failed || read["content"] != "# Accepted\n" {
		t.Fatalf("knowledge Git read failed: %v %v", read, failed)
	}
	_, failed = mcp("github_list_issues", map[string]any{
		"project_id": "demo", "repository": source,
	})
	if !failed {
		t.Fatal("local bare Git advertised GitHub issues")
	}
	// A restart retains the knowledge destination in SQLite, not an implicit
	// copy of the knowledge document.
	policy, err := db.KnowledgePolicy("demo")
	if err != nil || policy["repository"] != source {
		t.Fatalf("project knowledge configuration lost: %v %v", policy, err)
	}
}
