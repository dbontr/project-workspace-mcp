package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupResticTest(t *testing.T) (ResticRepositoryConfig, string) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic binary not installed")
	}
	root := t.TempDir()
	password := filepath.Join(root, "restic-password")
	if err := os.WriteFile(password, []byte("temporary-test-password-123456789\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "encrypted")
	conf := ResticRepositoryConfig{Repository: repo, PasswordFile: password}
	cmd := conf.resticCommand(context.Background(), root, "init")
	if err := cmd.Run(); err != nil {
		t.Fatalf("restic init failed: %v", err)
	}
	return conf, root
}

func TestEncryptedOffsiteBackupRestoreAndVerification(t *testing.T) {
	conf, root := setupResticTest(t)
	db, err := OpenDatabase(filepath.Join(root, "source", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	saved, err := db.ArtifactSave("research", "theorem.txt", []byte("original signed proof"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Personal notebook", "Private task", []string{"research"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SavePersonalTaskText("controller-owner", asString(task["id"]), "scratch.txt", "do not lose this work", 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	snapshot, err := db.BackupOffsite(ctx, conf)
	if err != nil || !resticSnapshotID.MatchString(snapshot) {
		t.Fatalf("encrypted backup failed: %q %v", snapshot, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := filepath.Join(root, "independent-host", "state.db")
	if err := RestoreOffsite(ctx, conf, snapshot, recovered); err != nil {
		t.Fatal(err)
	}
	check, err := OpenDatabase(recovered)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	a, err := check.ArtifactInfo("research", asString(saved["id"]))
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(check.artifactPath(asString(a["id"])))
	if err != nil || string(contents) != "original signed proof" {
		t.Fatalf("project artifact absent after offsite restore: %q %v", contents, err)
	}
	owner, err := check.PersonalTaskInfo("controller-owner", asString(task["id"]))
	if err != nil || owner["title"] != "Personal notebook" {
		t.Fatalf("personal task missing from offsite backup: %v %v", owner, err)
	}
	personal, err := check.PersonalTaskFileBase64("controller-owner", asString(task["id"]), "scratch.txt", 1)
	if err != nil || personal["sha256"] != checksum([]byte("do not lose this work")) {
		t.Fatalf("personal task bytes missing: %v %v", personal, err)
	}
	if err := VerifyOffsite(ctx, conf, snapshot); err != nil {
		t.Fatalf("full remote read and recovery verification: %v", err)
	}
	if err := RestoreOffsite(ctx, conf, snapshot, recovered); err == nil {
		t.Fatal("offsite restore cannot overwrite a populated database")
	}
	if err := RestoreOffsite(ctx, conf, "latest", filepath.Join(root, "unsafe", "state.db")); err == nil {
		t.Fatal("implicit latest snapshot must never be accepted")
	}
}

func TestResticCredentialsAndMissingBackend(t *testing.T) {
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(password, 0644); err != nil {
		t.Fatal(err)
	}
	cfg := ResticRepositoryConfig{Repository: "/tmp/unused", PasswordFile: password}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("world-readable Restic secret must be refused: %v", err)
	}
	if err := os.Chmod(password, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.PasswordFile = "./password"
	if err := cfg.Validate(); err == nil {
		t.Fatal("relative password location should fail")
	}
	cfg.PasswordFile = password
	cfg.Repository = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("missing repository should fail closed")
	}
	if err := cfg.run(context.Background(), "", "check"); err == nil {
		t.Fatal("repository command must fail without a configured backend")
	}
}
