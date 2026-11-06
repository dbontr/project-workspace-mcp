package controller

import (
	"encoding/base64"
	"testing"
)

func TestResearchAttachmentAdmissionAndStudyPackCitations(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	if _, err := db.CreateLibrary("science", "Science", "", "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkLibrary("demo", "science"); err != nil {
		t.Fatal(err)
	}
	original := []byte("%PDF-1.4 supplied bytes")
	digest := checksum(original)
	if _, err := db.StoreMCPResearchAttachment("science", "notes.pdf", "paper",
		digest, base64.StdEncoding.EncodeToString(original)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StoreMCPResearchAttachment("science", "forged.pdf", "paper",
		digest, base64.StdEncoding.EncodeToString([]byte("not the same"))); err == nil {
		t.Fatal("forged attachment accepted")
	}
	prepared := []byte(`{"title":"Energy review","questions":["Why conserved?"],"sources":[{"name":"notes.pdf"}]}`)
	if _, err := db.StoreMCPResearchAttachment("science", "energy-review.json", "study-pack",
		checksum(prepared), base64.StdEncoding.EncodeToString(prepared)); err != nil {
		t.Fatalf("prepared study pack upload rejected: %v", err)
	}
	source, err := db.RegisterResearchSource(ResearchSourceRequest{
		Library: "science", Title: "Lecture", OriginKind: "object", OriginName: "notes.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.IngestResearchPassages("science", asString(source["id"]), "append", 0,
		[]ResearchPassageInput{{Ordinal: 1, Page: 3, Section: "Energy",
			Text: "Conservation of energy constrains a closed system."}})
	if err != nil {
		t.Fatal(err)
	}
	found, err := db.SearchResearch("demo", "conservation", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %v %v", found, err)
	}
	pack, err := db.PrepareStudyPack("demo", "Energy", "Reconstruct the argument",
		[]string{asString(found[0]["id"])}, []string{"What is conserved?"})
	if err != nil || pack["stored"] != false || pack["source_verified"] != true {
		t.Fatalf("study pack: %v %v", pack, err)
	}
	passages := pack["passages"].([]map[string]any)
	if passages[0]["page"] != float64(3) && asInt(passages[0]["page"]) != 3 {
		t.Fatalf("page locator missing: %v", passages[0])
	}
	if passages[0]["original_sha256"] != digest {
		t.Fatal("study packet did not preserve original provenance")
	}
	if _, err := db.PrepareStudyPack("demo", "Forgeries", "", []string{"forged"}, nil); err == nil {
		t.Fatal("accepted nonexistent source")
	}
}
