package controller

import "testing"

func TestRepositoryRebindPreservesSourceIdentityAndKnowledgeDestination(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkRepositoryWithRole("research", "example/source", "source", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLibrary("archive", "Archive", "", "research"); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(`INSERT INTO library_sources(library,repository,revision,path,kind,added)
		VALUES('archive','example/source',?,'','source',?)`,
		"1111111111111111111111111111111111111111", stamp()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigureKnowledgePolicy("research", "git", "example/source", "main", "knowledge", 0); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(`INSERT INTO workspaces(id,project,node,image,cpu,memory_mb,status,expires,created,updated)
		VALUES('ws1','research','worker-one','debian:bookworm-slim',1,512,'stopped',0,?,?)`,
		stamp(), stamp()); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(`INSERT INTO workspace_repositories(workspace,repository,revision)
		VALUES('ws1','example/source',?)`, "1111111111111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	history, err := db.ContributeProject("research", ContributionRequest{
		Kind: "finding", Title: "Result", Body: "Immutable source record",
		Repository: "example/source", Revision: "1111111111111111111111111111111111111111",
	}, "controller-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CheckRepositoryRebind("example/source", "example/renamed"); err != nil {
		t.Fatalf("valid repository rename denied: %v", err)
	}
	counts, err := db.RebindRepositoryReferences("example/source", "example/renamed")
	if err != nil || counts["reconciled"] != true || asInt(counts["linked_projects"]) != 1 {
		t.Fatalf("repository transition: %v %v", counts, err)
	}
	if db.RepositoryLinked("research", "example/source") ||
		!db.RepositoryLinked("research", "example/renamed") {
		t.Fatal("project repository link was not reconciled")
	}
	policy, err := db.KnowledgePolicy("research")
	if err != nil || policy["repository"] != "example/renamed" {
		t.Fatalf("knowledge destination is stale: %v %v", policy, err)
	}
	for query, want := range map[string]string{
		"SELECT repository FROM library_sources WHERE library='archive'":      "example/renamed",
		"SELECT repository FROM workspace_repositories WHERE workspace='ws1'": "example/renamed",
	} {
		if want == "" {
			continue
		}
		row, err := db.queryOne(query)
		if err != nil || asString(row["repository"]) != want {
			t.Fatalf("stale repository for %s: %v %v", query, row, err)
		}
	}
	original, err := db.ProjectContribution("research", asString(history["id"]))
	if err != nil || original["repository"] != "example/source" {
		t.Fatalf("immutable history rewritten: %v %v", original, err)
	}
}

func TestRepositoryRebindRejectsConflictsAndActiveWorkspaces(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"example/source", "example/renamed"} {
		if _, err := db.LinkRepository("research", repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CheckRepositoryRebind("example/source", "example/renamed"); err == nil {
		t.Fatal("repository rename accepted conflicting destination")
	}
	if _, err := db.RebindRepositoryReferences("example/source", "example/renamed"); err == nil {
		t.Fatal("repository rebind accepted a conflicting destination")
	}
	if !db.RepositoryLinked("research", "example/source") {
		t.Fatal("failed transition changed source records")
	}
	if err := db.exec("DELETE FROM repositories WHERE full_name=?", "example/renamed"); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(`INSERT INTO workspaces(id,project,node,image,cpu,memory_mb,status,expires,created,updated)
		VALUES('ws-active','research','worker-one','debian:bookworm-slim',1,512,'ready',0,?,?)`,
		stamp(), stamp()); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("INSERT INTO workspace_repositories(workspace,repository,revision) VALUES(?,?,?)",
		"ws-active", "example/source", "1111111111111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckRepositoryRebind("example/source", "example/renamed"); err == nil {
		t.Fatal("active workspace was not protected from provider mutation")
	}
}
