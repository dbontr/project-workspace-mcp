package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dbontr/project-workspace-mcp/internal/worker"
)

func await(t *testing.T, limit time.Duration, check func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		ok, e := check()
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("timed out waiting for worker acknowledgement")
}
func TestDockerControllerWorker(t *testing.T) {
	if os.Getenv("RUN_DOCKER_INTEGRATION") != "1" {
		t.Skip("requires an explicit Linux Docker integration runner")
	}
	if _, e := exec.LookPath("docker"); e != nil {
		t.Skip("Docker unavailable")
	}
	if e := exec.Command("docker", "image", "inspect", "python:3.12-slim").Run(); e != nil {
		t.Fatal("Docker image python:3.12-slim not present")
	}
	db, _, srv := fixture(t)
	project(t, srv)
	t.Setenv("PWM_ALLOWED_IMAGES", "python:3.12-slim")
	runner := worker.NewRunner(nil)
	capabilities, e := worker.Capabilities(runner.AvailableDevices())
	if e != nil {
		t.Fatal(e)
	}
	c, pairing := call(t, srv, "POST", "/api/pairing", testOwner, nil)
	wantOK(t, c, pairing)
	c, enrolled := call(t, srv, "POST", "/api/pair", "", map[string]any{"name": "worker-one", "pairing_code": pairing["pairing_code"], "capabilities": capabilities})
	wantOK(t, c, enrolled)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &worker.Client{Base: srv.URL, Node: "worker-one", Token: asString(enrolled["worker_token"]), HTTP: &http.Client{Timeout: 90 * time.Second}}
	finished := make(chan error, 1)
	go func() { finished <- client.Run(ctx, runner) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
	})
	created, e := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", Image: "python:3.12-slim", CPU: 1, MemoryMB: 512, DurationMinutes: 5})
	if e != nil {
		t.Fatal(e)
	}
	wid := asString(created["id"])
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "pwm-"+wid).Run()
		_ = exec.Command("docker", "volume", "rm", "pwm-"+wid).Run()
	})
	await(t, 70*time.Second, func() (bool, error) {
		row, e := db.Workspace("demo", wid)
		return e == nil && row["status"] == "ready", e
	})
	put, e := db.CallWorkspace("demo", wid, "write_file", map[string]any{"path": "answer.py", "content": "print(6*7)\n"})
	if e != nil {
		t.Fatal(e)
	}
	await(t, 50*time.Second, func() (bool, error) {
		row, e := db.Operation("demo", wid, asString(put["operation_id"]))
		return e == nil && row["status"] == "succeeded", e
	})
	run, e := db.CallWorkspace("demo", wid, "command", map[string]any{"command": "python answer.py"})
	if e != nil {
		t.Fatal(e)
	}
	await(t, 50*time.Second, func() (bool, error) {
		row, e := db.Operation("demo", wid, asString(run["operation_id"]))
		if e != nil {
			return false, e
		}
		if row["status"] == "failed" {
			t.Errorf("Docker operation failed: %v", row["result"])
			return true, nil
		}
		if row["status"] == "succeeded" {
			res := gitObj(row["result"])
			if res["stdout"] != "42\n" || asInt(res["exit_code"]) != 0 {
				t.Errorf("unexpected command result: %v", res)
			}
			return true, nil
		}
		return false, nil
	})
	snap, e := db.CallWorkspace("demo", wid, "snapshot", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	await(t, 70*time.Second, func() (bool, error) {
		row, e := db.Operation("demo", wid, asString(snap["operation_id"]))
		if e != nil {
			return false, e
		}
		if row["status"] == "failed" {
			t.Errorf("snapshot failed: %v", row["result"])
			return true, nil
		}
		return row["status"] == "succeeded", nil
	})
	list, e := db.SnapshotList("demo", wid)
	if e != nil || len(list) != 1 {
		t.Fatalf("missing snapshot %v %v", list, e)
	}
	if _, e = db.CheckpointStopWorkspace("demo", wid); e != nil {
		t.Fatal(e)
	}
	await(t, 70*time.Second, func() (bool, error) {
		row, e := db.Workspace("demo", wid)
		if e != nil {
			return false, e
		}
		if row["status"] == "failed" {
			return false, fmt.Errorf("checkpoint teardown failed")
		}
		return row["status"] == "stopped", nil
	})
	checkpoint, e := db.CheckpointStopStatus("demo", wid)
	if e != nil || checkpoint["status"] != "stopped" {
		t.Fatalf("checkpointed stop did not finish: %v %v", checkpoint, e)
	}
	prune, e := db.PruneWorkspaceVolume("demo", wid, asString(checkpoint["snapshot_id"]), true)
	if e != nil {
		t.Fatal(e)
	}
	await(t, 50*time.Second, func() (bool, error) {
		row, e := db.Operation("demo", wid, asString(prune["operation_id"]))
		if e != nil {
			return false, e
		}
		if row["status"] == "failed" {
			return false, fmt.Errorf("volume prune failed: %v", row["result"])
		}
		return row["status"] == "succeeded", nil
	})
	if e := exec.Command("docker", "volume", "inspect", "pwm-"+wid).Run(); e == nil {
		t.Fatal("pruned workspace volume still exists")
	}
}
func TestDockerVirtualDisplayCapture(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" {
		t.Skip("requires an explicit virtual-display Docker integration runner")
	}
	if err := exec.Command("docker", "image", "inspect", "pwm-env-display:v1").Run(); err != nil {
		t.Fatal("Docker display image pwm-env-display:v1 not present")
	}
	db, _, srv := fixture(t)
	project(t, srv)
	t.Setenv("PWM_ALLOWED_IMAGES", "pwm-env-display:v1")
	runner := worker.NewRunner(nil)
	capabilities, err := worker.Capabilities(runner.AvailableDevices())
	if err != nil {
		t.Fatal(err)
	}
	capabilities["images"] = runner.ApprovedImages()
	code, pairing := call(t, srv, "POST", "/api/pairing", testOwner, nil)
	wantOK(t, code, pairing)
	code, enrolled := call(t, srv, "POST", "/api/pair", "", map[string]any{
		"name": "worker-one", "pairing_code": pairing["pairing_code"], "capabilities": capabilities,
	})
	wantOK(t, code, enrolled)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &worker.Client{
		Base: srv.URL, Node: "worker-one",
		Token: asString(enrolled["worker_token"]), HTTP: &http.Client{Timeout: 90 * time.Second},
	}
	finished := make(chan error, 1)
	go func() { finished <- client.Run(ctx, runner) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
	})

	created, err := db.CreateWorkspace(WorkspaceRequest{
		ProjectID: "demo", Image: "pwm-env-display:v1", CPU: 2,
		MemoryMB: 2048, DurationMinutes: 8, Display: true, DisplayWidth: 800, DisplayHeight: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := asString(created["id"])
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "pwm-"+ws).Run()
		_ = exec.Command("docker", "volume", "rm", "pwm-"+ws).Run()
	})
	await(t, 110*time.Second, func() (bool, error) {
		row, err := db.Workspace("demo", ws)
		if err != nil {
			return false, err
		}
		if row["status"] == "failed" {
			return false, fmt.Errorf("virtual display failed to start: %v", row)
		}
		return row["status"] == "ready", nil
	})

	launch, err := db.CallWorkspace("demo", ws, "command",
		map[string]any{"command": "xclock >/tmp/xclock.log 2>&1 &"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, 70*time.Second, func() (bool, error) {
		row, err := db.Operation("demo", ws, asString(launch["operation_id"]))
		if err != nil {
			return false, err
		}
		if row["status"] == "failed" {
			return false, fmt.Errorf("X11 application launch failed: %v", row["result"])
		}
		return row["status"] == "succeeded", nil
	})
	time.Sleep(time.Second)
	screenshot, err := db.CallWorkspace("demo", ws, "capture_display", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var artifact string
	await(t, 70*time.Second, func() (bool, error) {
		row, err := db.Operation("demo", ws, asString(screenshot["operation_id"]))
		if err != nil {
			return false, err
		}
		if row["status"] == "failed" {
			return false, fmt.Errorf("display screenshot failed: %v", row["result"])
		}
		if row["status"] != "succeeded" {
			return false, nil
		}
		result := gitObj(row["result"])
		artifact = asString(gitObj(result["artifact"])["id"])
		if artifact == "" || asInt(result["width"]) != 800 || asInt(result["height"]) != 600 {
			return false, fmt.Errorf("invalid screenshot receipt: %v", result)
		}
		return true, nil
	})
	viewed, err := db.ReadPNGImage("demo", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(viewed.Metadata["width"]) != 800 || asInt(viewed.Metadata["height"]) != 600 {
		t.Fatalf("captured image did not match virtual display: %v", viewed.Metadata)
	}
	bytesPNG, err := base64.StdEncoding.DecodeString(viewed.Data)
	if err != nil {
		t.Fatal(err)
	}
	pixels, err := png.Decode(bytes.NewReader(bytesPNG))
	if err != nil {
		t.Fatal(err)
	}
	baseR, baseG, baseB, _ := pixels.At(0, 0).RGBA()
	var painted bool
	for y := 0; y < 600 && !painted; y += 10 {
		for x := 0; x < 800; x += 10 {
			r, g, b, _ := pixels.At(x, y).RGBA()
			if r != baseR || g != baseG || b != baseB {
				painted = true
				break
			}
		}
	}
	if !painted {
		t.Fatal("captured display has no visible GUI content")
	}
	if _, err := db.StopWorkspace("demo", ws); err != nil {
		t.Fatal(err)
	}
	await(t, 70*time.Second, func() (bool, error) {
		row, err := db.Workspace("demo", ws)
		return err == nil && row["status"] == "stopped", err
	})
}
