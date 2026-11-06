package worker

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotEntryLayout(t *testing.T) {
	for _, entry := range []string{".", "./", "./data", "./data/proof.txt"} {
		if !safeSnapshotEntry(entry) {
			t.Errorf("valid snapshot entry rejected: %q", entry)
		}
	}
	for _, entry := range []string{"", "data", "/data", "../data", "./../data", "./data/../proof.txt"} {
		if safeSnapshotEntry(entry) {
			t.Errorf("invalid snapshot entry accepted: %q", entry)
		}
	}
}

func TestSnapshotExtractionPreservesWorkspaceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Kubernetes workspace image uses GNU tar on Linux")
	}
	tar, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("tar executable unavailable")
	}
	version, err := exec.Command(tar, "--version").Output()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Skip("GNU tar is required for the Kubernetes runtime")
	}

	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data", "proof.txt"), []byte("checkpoint integrity"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0555); err != nil {
		t.Fatal(err)
	}
	snapshot, err := exec.Command(tar, "-czf", "-", "-C", source, ".").Output()
	if err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	if err := os.Chmod(destination, 0750); err != nil {
		t.Fatal(err)
	}
	args := snapshotExtractArgs(destination)
	command := exec.Command(args[0], args[1:]...)
	command.Stdin = bytes.NewReader(snapshot)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("rootless snapshot extraction failed: %v: %s", err, output)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 {
		t.Fatalf("workspace root permissions changed: %v", info.Mode().Perm())
	}
	content, err := os.ReadFile(filepath.Join(destination, "data", "proof.txt"))
	if err != nil || string(content) != "checkpoint integrity" {
		t.Fatalf("restored file invalid: %v %q", err, content)
	}
}
