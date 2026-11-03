package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKubernetes(t *testing.T) (*Kubernetes, string) {
	t.Helper()
	dir := t.TempDir()
	trace := filepath.Join(dir, "calls.log")
	manifest := filepath.Join(dir, "manifests.jsonl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(trace) + "\n" +
		"case \"$*\" in\n" +
		"  *'create -f -'*) cat >> " + shellQuote(manifest) + "; echo >> " + shellQuote(manifest) + " ;;\n" +
		"  *'get job pwm-job-'*) echo '{\"status\":{\"succeeded\":1}}' ;;\n" +
		"  *'logs job/pwm-job-'*) echo 'job completed' ;;\n" +
		"  *'get pod pwm-'*) echo '{\"metadata\":{\"labels\":{\"app.kubernetes.io/managed-by\":\"project-workspace\",\"pwm.dev/project\":\"research\",\"pwm.dev/worker\":\"worker-one\",\"pwm.dev/kind\":\"workspace\",\"pwm.dev/workspace\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}}}' ;;\n" +
		"  *'get pvc pwm-data-'*) echo '{\"metadata\":{\"labels\":{\"app.kubernetes.io/managed-by\":\"project-workspace\",\"pwm.dev/project\":\"research\",\"pwm.dev/worker\":\"worker-one\",\"pwm.dev/kind\":\"volume\",\"pwm.dev/workspace\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}}}' ;;\n" +
		"  *'get pods -l pwm.dev/workspace='*) echo '{\"items\":[]}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	k, err := NewKubernetes("pwm-research", "research", 8, 16384, 4)
	if err != nil {
		t.Fatal(err)
	}
	k.Worker = "worker-one"
	return k, trace
}

func TestKubernetesNamespaceAndWorkloadPolicy(t *testing.T) {
	k, _ := testKubernetes(t)
	id := strings.Repeat("a", 32)
	pod := k.workspacePod(id, "debian:bookworm-slim", 2, 2048, true, 1440, 900, nil)
	meta := kubeObject(pod, "metadata")
	if meta["name"] != "pwm-"+id || !k.owns(pod, id, "workspace") {
		t.Fatal("workspace Pod ownership not enforced")
	}
	spec := kubeObject(pod, "spec")
	if spec["automountServiceAccountToken"] != false ||
		spec["serviceAccountName"] != "pwm-sandbox" {
		t.Fatal("workspace Pods must not inherit Kubernetes API tokens")
	}
	containers := spec["containers"].([]any)
	container := containers[0].(map[string]any)
	security := kubeObject(container, "securityContext")
	if security["runAsNonRoot"] != true ||
		security["allowPrivilegeEscalation"] != false ||
		security["readOnlyRootFilesystem"] != true {
		t.Fatalf("unsafe workspace Pod security: %v", security)
	}
	resources := kubeObject(container, "resources")
	limits := kubeObject(resources, "limits")
	if limits["cpu"] != "2000m" || limits["memory"] != "2048Mi" {
		t.Fatalf("Kubernetes resource enforcement missing: %v", limits)
	}
	if kubeObject(k.claim(id), "spec")["accessModes"] == nil {
		t.Fatal("workspace has no persistent volume claim")
	}
	if _, err := NewKubernetes("../system", "research", 8, 16384, 4); err == nil {
		t.Fatal("accepted cross-namespace configuration")
	}
}

func TestKubernetesWorkspaceLifecycleUsesAPI(t *testing.T) {
	k, trace := testKubernetes(t)
	r := NewRunner(nil)
	r.Kube = k
	id := strings.Repeat("a", 32)
	start, err := k.Start(context.Background(), r, id, map[string]any{
		"image": "debian:bookworm-slim", "cpu": 1.0, "memory_mb": 512.0,
	})
	if err != nil || start["status"] != "ready" {
		t.Fatalf("workspace Kubernetes creation failed: %v %v", start, err)
	}
	if _, err := k.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := k.PruneVolume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	operations, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"create -f -", "wait --for=condition=Ready",
		"exec pod/pwm-" + id, "delete pod pwm-" + id, "delete pvc pwm-data-" + id} {
		if !strings.Contains(string(operations), expected) {
			t.Fatalf("missing API operation %q:\n%s", expected, operations)
		}
	}
}

func TestKubernetesJobUsesBatchAPIAndCleansUp(t *testing.T) {
	k, trace := testKubernetes(t)
	r := NewRunner(nil)
	r.Kube = k
	job := Job{ID: strings.Repeat("b", 32), Image: "debian:bookworm-slim",
		CPU: 1, MemoryMB: 512, Command: "echo job completed"}
	status, output := k.ExecuteJob(context.Background(), r, job)
	if status != "succeeded" || !strings.Contains(output, "job completed") {
		t.Fatalf("Kubernetes Job failed: %s %s", status, output)
	}
	traceData, _ := os.ReadFile(trace)
	if !strings.Contains(string(traceData), "delete job pwm-job-"+job.ID) {
		t.Fatalf("Kubernetes Job was not cleaned up: %s", traceData)
	}
	jobSpec := k.jobManifest(job, k.Worker)
	if jobSpec["apiVersion"] != "batch/v1" ||
		kubeObject(jobSpec, "spec")["backoffLimit"] != 0 {
		t.Fatalf("job lacks Kubernetes native retry policy: %v", jobSpec)
	}
}

func TestKubernetesDevicePluginPolicy(t *testing.T) {
	k, _ := testKubernetes(t)
	if _, err := k.hardwareResources(map[string]any{"usb_passthrough": true}); err == nil {
		t.Fatal("host USB passthrough was accepted in a Kubernetes sandbox")
	}
	if _, err := k.hardwareResources(map[string]any{
		"devices": []any{map[string]any{"id": "gpu-0", "kind": "gpu"}},
	}); err == nil {
		t.Fatal("GPU allocation accepted without a configured device-plugin resource")
	}
	k.Devices = []KubernetesDevice{{ID: "gpu-0", Kind: "gpu", Resource: "nvidia.com/gpu"}}
	assignment, err := k.hardwareResources(map[string]any{
		"devices": []any{map[string]any{"id": "gpu-0", "kind": "gpu"}},
	})
	if err != nil || assignment["nvidia.com/gpu"] != "1" {
		t.Fatalf("GPU allocation failed to request device-plugin resource: %v %v", assignment, err)
	}
}

func TestKubernetesJobManifestDoesNotMountControllerCredentials(t *testing.T) {
	k, _ := testKubernetes(t)
	item := k.jobManifest(Job{ID: strings.Repeat("b", 32),
		Image: "debian:bookworm-slim", Command: "echo ok", CPU: 2, MemoryMB: 2048}, "worker-one")
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secretKeyRef", "hostPath", "hostNetwork", "privileged", "docker.sock"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("unsafe Kubernetes workload privilege: %s", forbidden)
		}
	}
}
func TestKubernetesWorkloadsUseNamespaceImagePullSecrets(t *testing.T) {
	k, _ := testKubernetes(t)
	k.ImagePullSecret = "pwm-private-registry"
	id := strings.Repeat("a", 32)
	workspace := k.workspacePod(id, "registry.example/research:1", 1, 512, false, 0, 0, nil)
	pulls := kubeObject(workspace, "spec")["imagePullSecrets"]
	if len(pulls.([]any)) != 1 || pulls.([]any)[0].(map[string]string)["name"] != k.ImagePullSecret {
		t.Fatalf("workspace image pull Secret not scoped to Pod: %v", pulls)
	}
	job := k.jobManifest(Job{ID: strings.Repeat("b", 32),
		Image: "registry.example/research:1", Command: "echo ok", CPU: 1, MemoryMB: 512}, k.Worker)
	template := kubeObject(kubeObject(job, "spec"), "template")
	podSpec := kubeObject(template, "spec")
	if len(podSpec["imagePullSecrets"].([]any)) != 1 {
		t.Fatalf("batch Job cannot pull private images: %v", job)
	}
	for _, pod := range []map[string]any{workspace, job} {
		serialized, err := json.Marshal(pod)
		if err != nil || bytes.Contains(serialized, []byte("secretKeyRef")) {
			t.Fatal("registry credentials were mounted as an application Secret")
		}
	}
}
