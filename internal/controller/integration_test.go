package controller

import (
	"context"
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
	if _, e = db.StopWorkspace("demo", wid); e != nil {
		t.Fatal(e)
	}
	await(t, 70*time.Second, func() (bool, error) {
		row, e := db.Workspace("demo", wid)
		return e == nil && row["status"] == "stopped", e
	})
}
