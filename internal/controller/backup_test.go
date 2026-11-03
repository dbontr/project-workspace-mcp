package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "original", "state.db")
	db, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	saved, err := db.ArtifactSave("research", "proof.txt", []byte("verified research data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.exec("INSERT INTO libraries(id,title,description,kind,created) VALUES(?,?,?,?,?)", "shared", "Shared", "", "lean", stamp()); err != nil {
		t.Fatal(err)
	}
	payload := []byte("content-addressed formalization")
	digest := checksum(payload)
	if err := os.MkdirAll(filepath.Dir(db.objectPath(digest)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db.objectPath(digest), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("INSERT INTO library_objects(library,name,sha256,size,kind,created) VALUES(?,?,?,?,?,?)", "shared", "theorem", digest, len(payload), "formalization", stamp()); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "snapshot")
	if err := db.BackupTo(backup); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restorePath := filepath.Join(root, "other-host", "state.db")
	if err := RestoreBackup(backup, restorePath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenDatabase(restorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rows, err := restored.Projects()
	if err != nil || len(rows) != 1 || asString(rows[0]["id"]) != "research" {
		t.Fatalf("restored projects: %v, %v", rows, err)
	}
	id := asString(saved["id"])
	data, err := os.ReadFile(restored.artifactPath(id))
	if err != nil || string(data) != "verified research data" {
		t.Fatalf("restored artifact: %q, %v", data, err)
	}
	data, err = os.ReadFile(restored.objectPath(digest))
	if err != nil || string(data) != string(payload) {
		t.Fatalf("restored research object: %q, %v", data, err)
	}
	if err := RestoreBackup(backup, restorePath); err == nil {
		t.Fatal("restore must not overwrite existing data")
	}
}

func TestBackupRejectsChangedAndMissingFiles(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	record, err := db.ArtifactSave("research", "result.txt", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err := db.BackupTo(backup); err != nil {
		t.Fatal(err)
	}
	id := asString(record["id"])
	file := filepath.Join(backup, "artifacts", id)
	if err := os.WriteFile(file, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(backup, filepath.Join(root, "restored", "state.db")); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("restore should reject modified artifact: %v", err)
	}
	if err := os.WriteFile(db.artifactPath(id), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.BackupTo(filepath.Join(root, "second-backup")); err == nil {
		t.Fatal("backup must reject corrupt source artifact")
	}
}

func TestBackupRejectsPendingUploads(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.exec("INSERT INTO libraries(id,title,description,kind,created) VALUES(?,?,?,?,?)", "shared", "Shared", "", "lean", stamp()); err != nil {
		t.Fatal(err)
	}
	if err := db.exec("INSERT INTO library_uploads(id,library,name,sha256,size,kind,received,created,expires) VALUES(?,?,?,?,?,?,?,?,?)",
		"0123456789abcdef0123456789abcdef", "shared", "pending", strings.Repeat("a", 64), 100, "source", 0, stamp(), stamp()+3600); err != nil {
		t.Fatal(err)
	}
	if err := db.BackupTo(filepath.Join(root, "snapshot")); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("pending upload should block snapshot: %v", err)
	}
}
