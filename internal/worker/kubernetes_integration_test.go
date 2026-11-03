package worker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestKubernetesLivePodPVCCreateExecuteJobAndCleanup(t *testing.T) {
	if os.Getenv("RUN_KUBE_INTEGRATION") != "1" {
		t.Skip("requires an operator-authorized Kubernetes test cluster")
	}
	namespace := "pwm-integration"
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("kubectl", args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("kubectl %v failed: %v\n%s", args, err, output)
		}
	}
	run("create", "namespace", namespace)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "delete", "namespace", namespace, "--wait=false").Run()
	})
	run("-n", namespace, "create", "serviceaccount", "pwm-sandbox")
	k, err := NewKubernetes(namespace, "research", 8, 16384, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Worker = "worker-one"
	r := NewRunner(nil)
	r.Kube = k
	r.Images = map[string]bool{"busybox:1.36": true}
	id := strings.Repeat("c", 32)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	result, err := k.Start(ctx, r, id, map[string]any{
		"image": "busybox:1.36", "cpu": 0.25, "memory_mb": float64(128),
	})
	if err != nil {
		t.Fatalf("Kubernetes workspace creation failed: %v", err)
	}
	if result["status"] != "ready" {
		t.Fatalf("Kubernetes workspace was not ready: %v", result)
	}
	pod, err := k.get(ctx, "pod", kubePodName(id))
	if err != nil || !k.owns(pod, id, "workspace") {
		t.Fatalf("Pod was not owned by this worker: %v %v", pod, err)
	}
	if node := kubeObject(pod, "spec")["nodeName"]; node == "" {
		t.Fatal("Kubernetes scheduler has not assigned the workspace Pod")
	}
	command, err := k.Command(ctx, id, "echo kubernetes-pod", nil)
	if err != nil || command["exit_code"] != 0 ||
		!strings.Contains(command["stdout"].(string), "kubernetes-pod") {
		t.Fatalf("Pod command failed: %v %v", command, err)
	}
	filename, err := k.writeFile(ctx, r, id, map[string]any{
		"path": "proof.txt", "content": "persistent data",
	})
	if err != nil || filename["sha256"] != HexSHA([]byte("persistent data")) {
		t.Fatalf("PVC write failed: %v %v", filename, err)
	}
	readBack, err := k.exec(ctx, id, "cat", "/workspace/proof.txt")
	if err != nil || strings.TrimSpace(string(readBack)) != "persistent data" {
		t.Fatalf("PVC readback failed: %s %v", readBack, err)
	}
	// A controller retry must reuse the original Pod and PVC without
	// resetting workspace files or accepting a changed immutable Pod request.
	if _, err := k.Start(ctx, r, id, map[string]any{
		"image": "busybox:1.36", "cpu": 0.25, "memory_mb": float64(128),
	}); err != nil {
		t.Fatalf("interrupted workspace start could not be recovered: %v", err)
	}
	if _, err := k.Start(ctx, r, id, map[string]any{
		"image": "busybox:1.36", "cpu": 0.5, "memory_mb": float64(128),
	}); err == nil {
		t.Fatal("workspace accepted changed immutable resources")
	}
	readBack, err = k.exec(ctx, id, "cat", "/workspace/proof.txt")
	if err != nil || strings.TrimSpace(string(readBack)) != "persistent data" {
		t.Fatalf("workspace contents were lost on repeated startup: %s %v", readBack, err)
	}
	job := Job{ID: strings.Repeat("d", 32),
		Image: "busybox:1.36", CPU: 0.25, MemoryMB: 128,
		Command: "echo kubernetes-job"}
	status, output := k.ExecuteJob(ctx, r, job)
	if status != "succeeded" || !strings.Contains(output, "kubernetes-job") {
		t.Fatalf("Kubernetes Job failed: %s %s", status, output)
	}
	if _, err := k.Stop(ctx, id); err != nil {
		t.Fatalf("Pod teardown failed: %v", err)
	}
	if _, err := k.Stop(ctx, id); err != nil {
		t.Fatalf("idempotent Pod teardown failed: %v", err)
	}
	if _, err := k.PruneVolume(ctx, id); err != nil {
		t.Fatalf("PVC cleanup failed: %v", err)
	}
}
