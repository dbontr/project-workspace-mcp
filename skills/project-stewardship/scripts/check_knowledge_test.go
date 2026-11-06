package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, filename, content string) {
	t.Helper()
	path := filepath.Join(root, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

const project = "# Project\n## Purpose\nResearch\n## Scope\nOne system\n## Acceptance criteria\nTests pass\n## Authorities\nGit\n"
const context = "# Context\n## Verified state\nTests pass\n## In progress\nNone\n## Blockers\nNone\n## Next action\nShip\n## References\nCommit\n"
const decision = "---\nid: source-authority-20261105\nkind: decision\nstatus: verified\ndate: 2026-11-05\n---\n# Decision: Source authority\n## Context\nNeed source\n## Decision\nGit\n## Alternatives\nFiles\n## Consequences\nCommits\n## Evidence\nVerified SHA\n"

func TestKnowledgeValidation(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Project.md", project)
	put(t, root, "Context.md", context)
	put(t, root, "decisions/2026-11-05-source-authority.md", decision)
	if got := checkDirectory(root, true); len(got) > 0 {
		t.Fatalf("valid records rejected: %v", got)
	}
	put(t, root, "findings/2026-11-05-result.md", strings.Replace(decision, "kind: decision", "kind: finding", 1))
	if got := checkDirectory(root, true); len(got) == 0 {
		t.Fatal("invalid record was accepted")
	}
}

func TestInvalidDatesAndDuplicateIDs(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Project.md", project)
	put(t, root, "Context.md", context)
	put(t, root, "decisions/2026-11-05-source-authority.md", decision)
	bad := strings.Replace(decision, "date: 2026-11-05", "date: 2026-02-30", 1)
	put(t, root, "decisions/2026-11-06-followup.md", bad)
	got := strings.Join(checkDirectory(root, true), "\n")
	if !strings.Contains(got, "duplicate record ID") || !strings.Contains(got, "date must be an ISO date") {
		t.Fatalf("missing errors: %s", got)
	}
}

func TestCLIAndDisabledMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	var out, errs bytes.Buffer
	if code := run([]string{root}, &out, &errs); code != 0 {
		t.Fatalf("disabled no-op rejected: %d %s", code, errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := run([]string{"--strict", root}, &out, &errs); code != 1 {
		t.Fatalf("missing required dir accepted: %d", code)
	}
}
