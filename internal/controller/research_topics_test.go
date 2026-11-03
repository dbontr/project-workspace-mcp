package controller

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestResearchTopicsScopedToVisibleEpochAndProject(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other", "Unlinked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLibrary("science", "Science", "", "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkLibrary("demo", "science"); err != nil {
		t.Fatal(err)
	}
	data := []byte("original research evidence")
	if _, err := db.StoreLibraryObject("science", "lecture.pdf", "paper", checksum(data),
		bytes.NewReader(data), 1<<20); err != nil {
		t.Fatal(err)
	}
	src, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "science", Title: "Electrostatics", OriginKind: "object", OriginName: "lecture.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := asString(src["id"])
	if _, err := db.IngestResearchPassages("science", id, "append", 0, []ResearchPassageInput{
		{Ordinal: 1, Page: 3, Text: "The electric field relates to scalar potential.",
			Topics: []string{"electrostatics", "vector-analysis"}},
		{Ordinal: 2, Page: 4, Text: "The gradient produces a vector from scalar potential.",
			Topics: []string{"vector-analysis"}},
	}); err != nil {
		t.Fatal(err)
	}
	topics, err := db.ResearchTopics("demo")
	if err != nil || len(topics) != 2 {
		t.Fatalf("expected indexed topics: %v %v", topics, err)
	}
	if topics[0]["topic"] != "electrostatics" || asInt(topics[0]["passages"]) != 1 ||
		topics[1]["topic"] != "vector-analysis" || asInt(topics[1]["passages"]) != 2 {
		t.Fatalf("incorrect topic counts: %v", topics)
	}
	browsed, err := db.ResearchTopicPassages("demo", "vector-analysis", 10)
	if err != nil || len(browsed) != 2 || browsed[0]["citation_ref"] == "" {
		t.Fatalf("missing passage citations: %v %v", browsed, err)
	}
	hidden, err := db.ResearchTopics("other")
	if err != nil || len(hidden) != 0 {
		t.Fatalf("unlinked project cannot list shared topics: %v %v", hidden, err)
	}
	if _, err := db.ResearchTopicPassages("demo", "vector-analysis') OR 1=1 --", 10); err == nil {
		t.Fatal("topic expression must not become SQL")
	}
	if _, err := db.IngestResearchPassages("science", id, "replace", 1, []ResearchPassageInput{
		{Ordinal: 1, Page: 7, Text: "Poisson's equation follows from divergence.",
			Topics: []string{"poisson-equation"}},
	}); err != nil {
		t.Fatal(err)
	}
	topics, err = db.ResearchTopics("demo")
	if err != nil || len(topics) != 1 || topics[0]["topic"] != "poisson-equation" {
		t.Fatalf("obsolete index topics must be invisible: %v %v", topics, err)
	}
	old, err := db.ReadResearchPassage("demo", asString(browsed[0]["id"]))
	if err != nil || old["origin_digest"] != checksum(data) {
		t.Fatalf("prior citation should remain verifiable: %v %v", old, err)
	}
	key, issuer := testOIDCIssuer(t, server)
	viewer := signedTestToken(t, key, issuer, "project-workspace", "reader", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#reader", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if status := researchGETStatus(t, httpServer, "/api/projects/demo/research/topics", viewer); status != http.StatusOK {
		t.Fatalf("authorized topic listing failed: %d", status)
	}
	if status := researchGETStatus(t, httpServer, "/api/projects/demo/research/topics/poisson-equation", viewer); status != http.StatusOK {
		t.Fatalf("authorized topic browse failed: %d", status)
	}
	if status := researchGETStatus(t, httpServer, "/api/projects/other/research/topics", viewer); status != http.StatusForbidden {
		t.Fatalf("cross-project topic enumeration must fail: %d", status)
	}
	code, rpc := call(t, httpServer, http.MethodPost, "/mcp/", viewer,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "research_browse_topic", "arguments": map[string]any{
				"project_id": "demo", "topic": "poisson-equation",
			}}})
	wantOK(t, code, rpc)
	result, _ := rpc["result"].(map[string]any)
	if asBool(result["isError"]) {
		t.Fatalf("MCP topic browsing should succeed: %v", result)
	}
}
