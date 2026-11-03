package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResearchSourceIndexAndProvenance(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateLibrary("science", "Physics library", "", "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkLibrary("demo", "science"); err != nil {
		t.Fatal(err)
	}
	original := []byte("source PDF bytes remain separate from derived text")
	hash := checksum(original)
	if _, err := db.StoreLibraryObject("science", "lecture.pdf", "paper", hash, bytes.NewReader(original), 1<<20); err != nil {
		t.Fatal(err)
	}
	source, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "science", Title: "Curvilinear coordinates",
		OriginKind: "object", OriginName: "lecture.pdf",
	})
	if err != nil || source["origin_digest"] != hash || asInt(source["index_revision"]) != 0 {
		t.Fatalf("original provenance missing: %v %v", source, err)
	}
	id := asString(source["id"])
	first, err := db.IngestResearchPassages("science", id, "append", 0, []ResearchPassageInput{
		{Ordinal: 1, Page: 2, Section: "Cylindrical basis", LineStart: 12, LineEnd: 18,
			Text:   "Curvilinear coordinates rotate the local unit basis with azimuth phi.",
			Topics: []string{"vector-analysis", "coordinates"}},
		{Ordinal: 2, Page: 3, Section: "Scale factors",
			Text: "Orthogonal coordinates have metric scale factors; cylindrical azimuth depends on radius."},
	})
	if err != nil || asInt(first["index_revision"]) != 1 {
		t.Fatalf("first indexing failed: %v %v", first, err)
	}
	found, err := db.SearchResearch("demo", "curvilinear basis", 20)
	if err != nil || len(found) != 1 {
		t.Fatalf("FTS lookup should locate passage: %v %v", found, err)
	}
	citation := asString(found[0]["citation_ref"])
	if !strings.Contains(citation, id+"@1:1") || asInt(found[0]["page"]) != 2 {
		t.Fatalf("source locators missing: %v", found[0])
	}
	read, err := db.ReadResearchPassage("demo", asString(found[0]["id"]))
	if err != nil || !strings.Contains(asString(read["body"]), "azimuth phi") ||
		read["origin_digest"] != hash {
		t.Fatalf("full passage provenance: %v %v", read, err)
	}
	if _, err := db.IngestResearchPassages("science", id, "append", 0, []ResearchPassageInput{
		{Ordinal: 3, Text: "outdated"},
	}); err == nil {
		t.Fatal("stale source index revision must fail")
	}
	if _, err := db.IngestResearchPassages("science", id, "append", 1, []ResearchPassageInput{
		{Ordinal: 1, Text: "duplicate ordinal in active epoch"},
	}); err == nil {
		t.Fatal("ordinal may not be reused within an active index epoch")
	}
	if _, err := db.IngestResearchPassages("science", id, "append", 1, []ResearchPassageInput{
		{Ordinal: 3, Text: "a finding about parallel transport and curvature"},
	}); err != nil {
		t.Fatal(err)
	}
	replaced, err := db.IngestResearchPassages("science", id, "replace", 2, []ResearchPassageInput{
		{Ordinal: 1, Page: 6, Text: "Covariant derivatives track how basis vectors vary across space."},
	})
	if err != nil || asInt(replaced["epoch"]) != 2 {
		t.Fatalf("replacement must advance index epoch without deleting evidence: %v %v", replaced, err)
	}
	active, err := db.SearchResearch("demo", "curvilinear basis", 20)
	if err != nil || len(active) != 0 {
		t.Fatalf("superseded index epoch must not surface in searches: %v %v", active, err)
	}
	historical, err := db.ReadResearchPassage("demo", asString(found[0]["id"]))
	if err != nil || historical["citation_ref"] != citation {
		t.Fatalf("old citation must remain resolvable: %v %v", historical, err)
	}
	if _, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "science", Title: "Forged object", OriginKind: "object", OriginName: "missing.pdf",
	}); err == nil {
		t.Fatal("cannot create an index without an original library object")
	}
	if _, err := db.IngestResearchPassages("science", id, "append", 3, []ResearchPassageInput{
		{Ordinal: 2, Text: "bad", Topics: []string{"duplicated", "duplicated"}},
	}); err == nil {
		t.Fatal("duplicate topics must fail")
	}
	if _, err := db.IngestResearchPassages("science", id, "append", 3, []ResearchPassageInput{
		{Ordinal: 2, LineStart: 8, LineEnd: 4, Text: "bad lines"},
	}); err == nil {
		t.Fatal("incorrect source line range must fail")
	}
	file, _, err := db.OpenLibraryObject("science", "lecture.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unchanged := make([]byte, len(original))
	if n, _ := file.Read(unchanged); n != len(original) || !bytes.Equal(unchanged, original) {
		t.Fatal("derived passage reindex must preserve the original source bytes")
	}
	if err := db.exec("UPDATE research_passages SET body=? WHERE id=?", "tampered", asString(found[0]["id"])); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadResearchPassage("demo", asString(found[0]["id"])); err == nil {
		t.Fatal("stored passage digest mismatch must fail")
	}
}

func TestResearchIndexPinnedGitAndCrossProjectAccess(t *testing.T) {
	db, server, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLibrary("lean-library", "Lean proofs", "", "lean"); err != nil {
		t.Fatal(err)
	}
	rev := strings.Repeat("a", 40)
	if _, err := db.AddLibrarySource("lean-library", "example/formalizer", rev, "Main.lean", "formalization"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkLibrary("demo", "lean-library"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "lean-library", Title: "Moving Git ref", OriginKind: "git",
		OriginName: "example/formalizer", OriginRevision: "main", OriginPath: "Main.lean",
	}); err == nil {
		t.Fatal("Git citations require an immutable commit")
	}
	source, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "lean-library", Title: "Formal proof", OriginKind: "git",
		OriginName: "example/formalizer", OriginRevision: rev, OriginPath: "Main.lean",
	})
	if err != nil || source["origin_digest"] != "" {
		t.Fatalf("Git commit should not be misstated as content digest: %v %v", source, err)
	}
	if _, err := db.IngestResearchPassages("lean-library", asString(source["id"]), "append", 0,
		[]ResearchPassageInput{{Ordinal: 1, Section: "theorem", LineStart: 4, LineEnd: 12,
			Text: "The theorem proves a bounded convergence relation."}}); err != nil {
		t.Fatal(err)
	}
	matches, err := db.SearchResearch("demo", "bounded convergence", 10)
	if err != nil || len(matches) != 1 || matches[0]["origin_revision"] != rev {
		t.Fatalf("Git revision not returned with search: %v %v", matches, err)
	}
	hidden, err := db.SearchResearch("other", "bounded convergence", 10)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("unlinked project must not retrieve shared index: %v %v", hidden, err)
	}
	if _, err := db.ReadResearchPassage("other", asString(matches[0]["id"])); err == nil {
		t.Fatal("cross-project passage read must fail")
	}
	key, issuer := testOIDCIssuer(t, server)
	editor := signedTestToken(t, key, issuer, "project-workspace", "reader", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", "oidc:"+issuer+"#reader", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	code := researchGETStatus(t, httpServer, "/api/projects/demo/research/search?q=bounded", editor)
	if code != http.StatusOK {
		t.Fatalf("project viewer cannot search linked research: %d", code)
	}
	code = researchGETStatus(t, httpServer, "/api/projects/other/research/search?q=bounded", editor)
	if code != http.StatusForbidden {
		t.Fatal("ungranted project must not expose search")
	}
	code, _ = call(t, httpServer, http.MethodPost, "/api/libraries/lean-library/research/sources", editor,
		ResearchSourceRequest{Title: "Forbidden", OriginKind: "git", OriginName: "example/formalizer",
			OriginRevision: rev, OriginPath: "Main.lean"})
	if code != http.StatusForbidden {
		t.Fatal("project viewer must not modify global shared-library index")
	}
	code = researchGETStatus(t, httpServer, "/api/projects/demo/research/sources", editor)
	if code != http.StatusOK {
		t.Fatalf("project viewer cannot list linked sources: %d", code)
	}
}

func TestResearchIndexBoundedLargeCorpus(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.CreateLibrary("large-source", "Large corpus", "", "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkLibrary("demo", "large-source"); err != nil {
		t.Fatal(err)
	}
	body := []byte("original remains durable")
	if _, err := db.StoreLibraryObject("large-source", "paper.pdf", "paper",
		checksum(body), bytes.NewReader(body), 1<<20); err != nil {
		t.Fatal(err)
	}
	source, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "large-source", Title: "Synthetic large corpus",
		OriginKind: "object", OriginName: "paper.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	for batch := 0; batch < 5; batch++ {
		passages := make([]ResearchPassageInput, 0, 64)
		for n := 0; n < 64; n++ {
			ordinal := batch*64 + n + 1
			passages = append(passages, ResearchPassageInput{
				Ordinal: ordinal, Page: 1 + ordinal/5,
				Text: fmt.Sprintf("Mathematical theorem number %d establishes a lemma in functional analysis.", ordinal),
			})
		}
		if _, err := db.IngestResearchPassages("large-source", asString(source["id"]),
			"append", batch, passages); err != nil {
			t.Fatalf("batch %d failed: %v", batch, err)
		}
	}
	found, err := db.SearchResearch("demo", "functional analysis", 100)
	if err != nil || len(found) != 100 {
		t.Fatalf("bounded search must return 100 of 320 passages: %d %v", len(found), err)
	}
	if _, err := db.SearchResearch("demo", "functional analysis", 101); err == nil {
		t.Fatal("search result limit must be bounded")
	}
	if _, err := db.SearchResearch("demo", "", 10); err == nil {
		t.Fatal("unbounded empty query must be rejected")
	}
}

func researchGETStatus(t *testing.T, server *httptest.Server, path, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}
