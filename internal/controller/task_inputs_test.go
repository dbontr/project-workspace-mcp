package controller

import (
	"encoding/base64"
	"testing"
)

func TestTaskAttachmentsAndStudyPacksRemainOwnerScoped(t *testing.T) {
	db, _, _ := fixture(t)
	task, err := db.CreatePersonalTask("controller-owner", "Study notes", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID := asString(task["id"])
	original := []byte("%PDF-1.4\nverified attachment bytes")
	encoded := base64.StdEncoding.EncodeToString(original)
	digest := checksum(original)
	if _, err := db.AdmitTaskAttachment("someone-else", taskID, "notes.pdf",
		"application/pdf", digest, encoded, 0); err == nil {
		t.Fatal("foreign task attachment accepted")
	}
	if _, err := db.AdmitTaskAttachment("controller-owner", taskID, "notes.pdf",
		"application/pdf", checksum([]byte("forged")), encoded, 0); err == nil {
		t.Fatal("attachment checksum mismatch accepted")
	}
	saved, err := db.AdmitTaskAttachment("controller-owner", taskID, "notes.pdf",
		"application/pdf", digest, encoded, 0)
	if err != nil || saved["parsed"] != false || saved["scope"] != "task" {
		t.Fatalf("attachment import: %v %v", saved, err)
	}
	source := StudyPackSource{ID: "lecture", Title: "Lecture", SHA256: digest, Location: "page 1"}
	pack := StudyPack{
		Title: "Midterm review", Objective: "Derive the main result",
		Sources:   []StudyPackSource{source},
		Questions: []StudyPackQuestion{{Prompt: "Explain the derivation", Answer: "Show each step", SourceIDs: []string{"missing"}}},
	}
	if _, err := db.SaveTaskStudyPack("controller-owner", taskID, "review.json", pack, 0); err == nil {
		t.Fatal("unmatched source citation accepted")
	}
	pack.Questions[0].SourceIDs = []string{"lecture"}
	result, err := db.SaveTaskStudyPack("controller-owner", taskID, "review.json", pack, 0)
	if err != nil || result["source_provenance_verified"] != false {
		t.Fatalf("prepared pack: %v %v", result, err)
	}
	files, err := db.PersonalTaskFiles("controller-owner", taskID)
	if err != nil || len(files) != 2 {
		t.Fatalf("task-owned imports missing: %v %v", files, err)
	}
	projects, err := db.Projects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("task import created project knowledge: %v %v", projects, err)
	}
}
