package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestControllerRetriesUnexpectedEOFButRejectsUnauthorized(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Token: "worker-token", Node: "worker-one", HTTP: server.Client()}
	err := client.request(context.Background(), "POST", "/api/worker/worker-one/heartbeat", map[string]any{"capabilities": map[string]any{"cpu": 1}}, nil)
	if err == nil || !retryableControllerError(err) {
		t.Fatalf("expected transient EOF: %v", err)
	}
	attempts := 0
	if err := retryController(context.Background(), err, &attempts); err != nil {
		t.Fatal(err)
	}
	if err := client.request(context.Background(), "POST", "/api/worker/worker-one/heartbeat", map[string]any{"capabilities": map[string]any{"cpu": 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("unexpected heartbeat count: %d", calls)
	}
	if retryableControllerError(&HTTPStatusError{Code: 401}) {
		t.Fatal("tried to retry invalid credential")
	}
	if !retryableControllerError(&HTTPStatusError{Code: 503}) {
		t.Fatal("did not retry temporary outage")
	}
}

func TestAgentContinuesAfterTransientHeartbeatEOF(t *testing.T) {
	var heartbeats int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/worker/worker-one/heartbeat":
			heartbeats++
			if heartbeats == 1 {
				c, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				c.Close()
				return
			}
			w.Write([]byte(`{"ok":true}`))
		case "/api/worker/worker-one/operation/claim":
			w.Write([]byte(`{"operation":null}`))
		case "/api/worker/worker-one/claim":
			w.Write([]byte(`{"job":null}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	client := &Client{Base: server.URL, Token: "token", Node: "worker-one", HTTP: server.Client()}
	err := client.Run(ctx, NewRunner(nil))
	if err != context.DeadlineExceeded {
		t.Fatalf("worker should stop on cancellation: %v", err)
	}
	if heartbeats < 2 {
		t.Fatalf("worker never recovered after connection closure: %d", heartbeats)
	}
}

func TestUSBPassthroughRequiresBothExplicitOptIns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "devices.json")
	policy := `{"devices":[{"id":"usb-stick","kind":"usb","paths":["/dev/disk/by-id/usb-Example_123"]}]}`
	if err := os.WriteFile(file, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	devices, err := LoadDevices(file)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(devices)
	if len(runner.AvailableDevices()) != 0 {
		t.Fatal("advertised USB while disabled")
	}
	payload := map[string]any{"image": "debian:bookworm-slim", "cpu": 1.0, "memory_mb": 512.0, "devices": []DeviceClaim{{ID: "usb-stick", Kind: "usb"}}, "usb_passthrough": false}
	if _, err := runner.Start(context.Background(), strings.Repeat("0", 32), payload); err == nil || !strings.Contains(err.Error(), "USB passthrough") {
		t.Fatalf("accepted disabled USB workspace: %v", err)
	}
	runner.USBEnabled = true
	payload["usb_passthrough"] = false
	if _, err := runner.Start(context.Background(), strings.Repeat("0", 32), payload); err == nil {
		t.Fatal("accepted workspace with USB toggle off")
	}
	payload["usb_passthrough"] = true
	if _, err := runner.Start(context.Background(), strings.Repeat("0", 32), payload); err == nil {
		t.Fatal("accepted a missing USB device")
	}
	for _, path := range []string{"/dev/sda", "/dev/null", "/etc/passwd", "/dev/disk/by-id/usb-Example-part1", "/dev/disk/by-id/ata-SYSTEM"} {
		if usbPathAllowed(path) {
			t.Fatalf("accepted unsafe USB path %s", path)
		}
	}
	if !usbPathAllowed("/dev/disk/by-id/usb-Example_123") || !usbPathAllowed("/dev/serial/by-id/usb-Adapter") || !usbPathAllowed("/dev/bus/usb/001/002") {
		t.Fatal("rejected a supported USB alias")
	}
	if _, err := LoadDevices(writeDeviceManifest(t, `{"devices":[{"id":"usb-stick","kind":"usb","paths":["/dev/sda"]}]}`)); err == nil {
		t.Fatal("accepted raw host disk path")
	}
}

func writeDeviceManifest(t *testing.T, payload string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestImportArtifactValidatesDestinationBeforeDownloading(t *testing.T) {
	runner := NewRunner(nil)
	id := strings.Repeat("a", 32)
	for _, target := range []string{"../device", "/etc/passwd", "images//disk.img"} {
		payload, _ := json.Marshal(map[string]any{"path": target, "artifact_id": id})
		_, err := runner.Handle(context.Background(), Operation{Workspace: id, Action: "import_file", Payload: payload}, nil)
		if err == nil || !strings.Contains(err.Error(), "workspace path") {
			t.Fatalf("unsafe artifact destination %q accepted: %v", target, err)
		}
	}
}

func TestWorkerHeartbeatContinuesDuringJobExecution(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nsleep 0.3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var heartbeats atomic.Int32
	var claimed atomic.Bool
	finished := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/worker/worker-one/heartbeat":
			heartbeats.Add(1)
			w.Write([]byte(`{"ok":true}`))
		case "/api/worker/worker-one/operation/claim":
			w.Write([]byte(`{"operation":null}`))
		case "/api/worker/worker-one/claim":
			if claimed.CompareAndSwap(false, true) {
				w.Write([]byte(`{"job":{"id":"cccccccccccccccccccccccccccccccc","command":"echo test","image":"debian:bookworm-slim","cpu":1,"memory_mb":128,"lease_token":"test-lease"}}`))
			} else {
				w.Write([]byte(`{"job":null}`))
			}
		case "/api/worker/worker-one/finish":
			select {
			case finished <- struct{}{}:
			default:
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Token: "test-token", Node: "worker-one", HTTP: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- client.runWithHeartbeatInterval(ctx, NewRunner(nil), 20*time.Millisecond)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("worker did not complete its assigned job")
	}
	if count := heartbeats.Load(); count < 3 {
		t.Errorf("node heartbeat stopped during job execution: %d reports", count)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker should stop when cancelled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down")
	}
}

func TestBackgroundHeartbeatRevocationStopsWorker(t *testing.T) {
	var heartbeats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/worker/worker-one/heartbeat":
			if heartbeats.Add(1) > 1 {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"detail":"revoked"}`))
				return
			}
			w.Write([]byte(`{"ok":true}`))
		case "/api/worker/worker-one/operation/claim":
			w.Write([]byte(`{"operation":null}`))
		case "/api/worker/worker-one/claim":
			w.Write([]byte(`{"job":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Token: "test-token", Node: "worker-one", HTTP: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.runWithHeartbeatInterval(ctx, NewRunner(nil), 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "worker heartbeat rejected") {
		t.Fatalf("worker did not stop on revoked heartbeat: %v", err)
	}
	if n := heartbeats.Load(); n < 2 {
		t.Fatalf("background heartbeat did not execute: %d", n)
	}
}

func TestInterruptedJobContainerCleanup(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logFile) + "\n" +
		"if [ \"$1\" = run ]; then exec sleep 4; fi\n" +
		"if [ \"$1\" = rm ] || [ \"$1\" = ps ]; then exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	jobID := strings.Repeat("a", 32)
	status, result := NewRunner(nil).ExecuteJob(ctx, Job{
		ID: jobID, Image: "debian:bookworm-slim", CPU: 1, MemoryMB: 128, Command: "echo test",
	})
	if status != "failed" || !strings.Contains(result, "deadline exceeded") {
		t.Fatalf("cancelled job result: status=%s result=%s", status, result)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(data)
	for _, part := range []string{
		"run --rm --name pwm-job-" + jobID + " --label project-workspace=job",
		"rm -f pwm-job-" + jobID,
		"ps -a --filter name=^/pwm-job-" + jobID + "$ --format {{.Names}}",
	} {
		if !strings.Contains(logged, part) {
			t.Fatalf("Docker operation %q missing from trace:\n%s", part, logged)
		}
	}
}

func TestDockerClientFailureRemovesManagedJob(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "docker.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logFile) + "\n" +
		"if [ \"$1\" = run ]; then exit 125; fi\n" +
		"if [ \"$1\" = rm ] || [ \"$1\" = ps ]; then exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	jobID := strings.Repeat("a", 32)
	status, result := NewRunner(nil).ExecuteJob(context.Background(), Job{
		ID: jobID, Image: "debian:bookworm-slim", CPU: 1, MemoryMB: 128, Command: "echo test",
	})
	if status != "failed" || !strings.Contains(result, "docker exit 125") {
		t.Fatalf("docker failure result: status=%s result=%s", status, result)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(data)
	for _, part := range []string{
		"run --rm --name pwm-job-" + jobID + " --label project-workspace=job",
		"rm -f pwm-job-" + jobID,
		"ps -a --filter name=^/pwm-job-" + jobID + "$ --format {{.Names}}",
	} {
		if !strings.Contains(logged, part) {
			t.Fatalf("Docker operation %q missing from trace:\n%s", part, logged)
		}
	}
}

func TestJobContainerIdentityAndCleanupVerification(t *testing.T) {
	if status, result := NewRunner(nil).ExecuteJob(context.Background(), Job{
		ID: "bad/name", Image: "debian:bookworm-slim",
	}); status != "failed" || result != "invalid job ID" {
		t.Fatalf("accepted unsafe Docker job name: %s %s", status, result)
	}

	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = run ]; then exec sleep 4; fi\n" +
		"if [ \"$1\" = ps ]; then printf '%s\\n' pwm-job-" + strings.Repeat("b", 32) + "; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	status, result := NewRunner(nil).ExecuteJob(ctx, Job{
		ID: strings.Repeat("b", 32), Image: "debian:bookworm-slim",
		CPU: 1, MemoryMB: 128, Command: "echo test",
	})
	if status != "failed" || !strings.Contains(result, "container remains") {
		t.Fatalf("failed to report orphaned job container: %s %s", status, result)
	}
}
