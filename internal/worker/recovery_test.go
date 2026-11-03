package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileStaleJobsTargetsOnlyWorkerContainers(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker.log")
	id := strings.Repeat("a", 32)
	container := "pwm-job-" + id
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logFile) + "\n" +
		"if [ \"$1\" = ps ]; then\n" +
		"  case \"$*\" in *\"label=project-workspace-worker=worker-one\"*) printf '%s\\n' " + shellQuote(container) + " ;; esac\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := NewRunner(nil)
	if err := runner.ReconcileStaleJobs(context.Background(), "worker-one"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	operations := string(data)
	for _, part := range []string{
		"ps -a --filter label=project-workspace=job --filter label=project-workspace-worker=worker-one",
		"rm -f " + container,
		"ps -a --filter name=^/" + container + "$",
	} {
		if !strings.Contains(operations, part) {
			t.Fatalf("missing Docker operation %q:\n%s", part, operations)
		}
	}
	if err := runner.ReconcileStaleJobs(context.Background(), "../bad"); err == nil {
		t.Fatal("accepted invalid worker identifier")
	}
}

func TestExecuteJobLabelsWorkerOwnership(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logFile) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := NewRunner(nil)
	runner.WorkerID = "worker-one"
	status, result := runner.ExecuteJob(context.Background(), Job{
		ID: strings.Repeat("b", 32), Image: "debian:bookworm-slim", CPU: 1,
		MemoryMB: 128, Command: "echo test",
	})
	if status != "succeeded" {
		t.Fatalf("job failed: %s", result)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--label project-workspace-worker=worker-one debian:bookworm-slim") {
		t.Fatalf("job does not carry worker ownership: %s", data)
	}
}
