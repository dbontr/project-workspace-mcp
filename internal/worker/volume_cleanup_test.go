package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeVolumeDocker(t *testing.T, labels, attached string) string {
	t.Helper()
	dir := t.TempDir()
	trace := filepath.Join(dir, "docker.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(trace) + "\n" +
		"if [ \"$1\" = volume ] && [ \"$2\" = inspect ] && [ \"$3\" = --format ]; then\n" +
		"  printf '%s\\n' " + shellQuote(labels) + "; exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = ps ]; then printf '%s' " + shellQuote(attached) + "; exit 0; fi\n" +
		"if [ \"$1\" = volume ] && [ \"$2\" = rm ]; then exit 0; fi\n" +
		"if [ \"$1\" = volume ] && [ \"$2\" = inspect ]; then\n" +
		"  echo 'Error: No such volume' >&2; exit 1\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return trace
}

func TestPruneVolumeChecksOwnershipAndAttachments(t *testing.T) {
	workspace := strings.Repeat("a", 32)
	labels := `{"project-workspace":"volume","project-workspace-workspace":"` + workspace + `"}`
	trace := fakeVolumeDocker(t, labels, "")
	result, err := NewRunner(nil).PruneVolume(context.Background(), workspace)
	if err != nil || result["status"] != "pruned" {
		t.Fatalf("volume should be removable: %v %v", result, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{
		"volume inspect --format {{json .Labels}} pwm-" + workspace,
		"ps -a --filter volume=pwm-" + workspace,
		"volume rm pwm-" + workspace,
		"volume inspect pwm-" + workspace,
	} {
		if !strings.Contains(string(data), step) {
			t.Fatalf("missing Docker step %q:\n%s", step, data)
		}
	}

	bad := fakeVolumeDocker(t, `{"project-workspace":"other"}`, "")
	if _, err := NewRunner(nil).PruneVolume(context.Background(), workspace); err == nil {
		t.Fatal("accepted unowned volume")
	}
	data, _ = os.ReadFile(bad)
	if strings.Contains(string(data), "volume rm") {
		t.Fatal("deleted a volume with unexpected labels")
	}

	attached := fakeVolumeDocker(t, labels, "container-still-attached")
	if _, err := NewRunner(nil).PruneVolume(context.Background(), workspace); err == nil {
		t.Fatal("accepted attached volume")
	}
	data, _ = os.ReadFile(attached)
	if strings.Contains(string(data), "volume rm") {
		t.Fatal("deleted volume referenced by a container")
	}
	if _, err := NewRunner(nil).PruneVolume(context.Background(), "../other"); err == nil {
		t.Fatal("accepted an unsafe volume name")
	}
}
