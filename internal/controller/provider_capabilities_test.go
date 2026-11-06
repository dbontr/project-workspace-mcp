package controller

import "testing"

func TestLocalSourceCanAttachIndependentGitHubPlanningRepository(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		repository string
		role       string
	}{
		{"local:lab/source", "source"},
		{"local:lab/notes", "research"},
		{"example/issue-tracker", "planning"},
	} {
		if _, err := db.LinkRepositoryWithRole("research", item.repository, item.role, "main"); err != nil {
			t.Fatalf("link %s: %v", item.repository, err)
		}
	}
	rows, err := db.RepositoryMemberships("research")
	if err != nil || len(rows) != 3 {
		t.Fatalf("independent planning link lost: %v %v", rows, err)
	}
	roles := map[string]string{}
	for _, row := range rows {
		roles[asString(row["repository"])] = asString(row["role"])
	}
	if roles["local:lab/source"] != "source" ||
		roles["local:lab/notes"] != "research" ||
		roles["example/issue-tracker"] != "planning" {
		t.Fatalf("provider roles were conflated: %v", roles)
	}
}
