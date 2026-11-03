package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointPolicy(t *testing.T) {
	for _, url := range []string{"https://compute.example", "http://127.0.0.1:8000", "http://localhost:8000"} {
		if _, err := ValidateURL(url); err != nil {
			t.Fatalf("rejected %s: %v", url, err)
		}
	}
	for _, url := range []string{"http://compute.example", "https://example.com/path", "https://abc?token=x", "file:///home", "https://user:pw@example.com"} {
		if _, err := ValidateURL(url); err == nil {
			t.Errorf("accepted unsafe URL %s", url)
		}
	}
}

func TestApprovedDeviceMappingOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "devices.json")
	manifest := `{"devices":[{"id":"gpu-1","kind":"gpu","model":"NVIDIA","cdi":"nvidia.com/gpu=GPU-a"},` +
		`{"id":"fpga-1","kind":"fpga","paths":["/dev/null"]}]}`
	if err := os.WriteFile(file, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	devices, err := LoadDevices(file)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := DeviceDockerArgs([]DeviceClaim{{ID: "gpu-1", Kind: "gpu"}, {ID: "fpga-1", Kind: "fpga"}}, devices)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 4 || flags[1] != "nvidia.com/gpu=GPU-a" || flags[3] != "/dev/null" {
		t.Fatalf("wrong Docker flags: %v", flags)
	}
	if _, err := DeviceDockerArgs([]DeviceClaim{{ID: "gpu-1", Kind: "fpga"}}, devices); err == nil {
		t.Fatal("GPU changed kind")
	}
	if _, err := DeviceDockerArgs([]DeviceClaim{{ID: "gpu-1", Kind: "gpu"}, {ID: "gpu-1", Kind: "gpu"}}, devices); err == nil {
		t.Fatal("double-leased GPU")
	}
	bad := `{"devices":[{"id":"p","kind":"fpga","paths":["/etc/shadow"]}]}`
	os.WriteFile(file, []byte(bad), 0600)
	if _, err := LoadDevices(file); err == nil {
		t.Fatal("accepted host file mapping")
	}
}

func TestWorkspacePathAndArchiveTraversal(t *testing.T) {
	for _, path := range []string{"../secret", "/etc/passwd", "x/../y", "x//y", "", "x/./y"} {
		if _, err := safePath(path); err == nil {
			t.Errorf("accepted workspace path %q", path)
		}
	}
	if got, err := safePath("src/main.go"); err != nil || got != "/workspace/src/main.go" {
		t.Fatalf("path: %q %v", got, err)
	}
	for _, name := range []string{"../../etc/shadow", "x/../etc/passwd", "/root/a"} {
		if safeArchiveEntry(name) {
			t.Errorf("accepted archive path %q", name)
		}
	}
	if !safeArchiveEntry("folder/main.go") {
		t.Fatal("rejected ordinary tar entry")
	}
}

func TestPairingAndFencedArtifactUpload(t *testing.T) {
	content := []byte("snapshot bytes")
	expected := sha256.Sum256(content)
	var posted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/pair":
			var req struct {
				Name string `json:"name"`
				Code string `json:"pairing_code"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Name != "worker-one" || req.Code != "single-use" {
				t.Errorf("unexpected join %v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"worker_token":"secret"}`))
		case strings.HasSuffix(r.URL.Path, "/artifact"):
			if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Operation-Token") != "lease-secret" {
				t.Errorf("missing authorization")
			}
			data := new(bytes.Buffer)
			data.ReadFrom(r.Body)
			if !bytes.Equal(data.Bytes(), content) {
				t.Errorf("upload content mismatch")
			}
			if r.Header.Get("X-Content-Sha256") != hex.EncodeToString(expected[:]) {
				t.Errorf("upload SHA mismatch")
			}
			posted = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"artifact-one","sha256":"` + hex.EncodeToString(expected[:]) + `"}`))
		default:
			t.Errorf("unexpected HTTP path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{Base: server.URL, Node: "worker-one", HTTP: server.Client()}
	token, err := c.Pair(context.Background(), "single-use", map[string]any{"cpu": 1})
	if err != nil {
		t.Fatal(err)
	}
	c.Token = token
	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "operation-one", Workspace: "workspace-one", LeaseToken: "lease-secret"}
	id, err := c.Upload(context.Background(), op, "snapshot.tar.gz", file, hex.EncodeToString(expected[:]))
	if err != nil || id != "artifact-one" || !posted {
		t.Fatalf("upload failed %s %v", id, err)
	}
}

func TestDockerBuildArgs(t *testing.T) {
	empty := dockerBuildArgs("none", "pwm-env-demo-test:v1", "-")
	if got := strings.Join(empty, " "); got != "build --network none --tag pwm-env-demo-test:v1 -" {
		t.Fatalf("invalid no-context build: %s", got)
	}
	context := dockerBuildArgs("none", "pwm-env-demo-test:v1", "/approved/toolchain")
	if got := strings.Join(context, " "); got != "build --network none --tag pwm-env-demo-test:v1 -f - /approved/toolchain" {
		t.Fatalf("invalid context build: %s", got)
	}
}
