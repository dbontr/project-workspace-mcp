package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesDeviceManifestAndSchedulerResource(t *testing.T) {
	k, _ := testKubernetes(t)
	filename := filepath.Join(t.TempDir(), "devices.json")
	manifest := `{"devices":[{"id":"gpu-0","kind":"gpu","resource":"nvidia.com/gpu","model":"GPU","memory_mb":12288},` +
		`{"id":"gpu-1","kind":"gpu","resource":"nvidia.com/gpu","model":"GPU","memory_mb":12288},` +
		`{"id":"fpga-0","kind":"fpga","resource":"example.com/fpga"}]}`
	if err := os.WriteFile(filename, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	devices, err := LoadKubernetesDevices(filename)
	if err != nil {
		t.Fatal(err)
	}
	k.Devices = devices
	capsRunner := NewRunner(nil)
	capsRunner.Kube = k
	caps, err := capsRunner.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if len(caps["devices"].([]any)) != 3 {
		t.Fatalf("device inventory missing from heartbeat: %v", caps)
	}
	resources, err := k.hardwareResources(map[string]any{
		"devices": []any{
			map[string]any{"id": "gpu-0", "kind": "gpu"},
			map[string]any{"id": "gpu-1", "kind": "gpu"},
			map[string]any{"id": "fpga-0", "kind": "fpga"},
		},
	})
	if err != nil || resources["nvidia.com/gpu"] != "2" || resources["example.com/fpga"] != "1" {
		t.Fatalf("Kubernetes device plugin request mismatch: %v %v", resources, err)
	}
	pod := k.workspacePod(strings.Repeat("a", 32), "debian:bookworm-slim", 2, 2048, false, 0, 0, resources)
	containers := kubeObject(pod, "spec")["containers"].([]any)
	allocation := kubeObject(containers[0].(map[string]any), "resources")
	limits := kubeObject(allocation, "limits")
	if limits["nvidia.com/gpu"] != "2" || limits["example.com/fpga"] != "1" {
		t.Fatalf("Pod lacks scheduler-enforced extended resources: %v", limits)
	}
	bad := []any{map[string]any{"id": "not-approved", "kind": "gpu"}}
	if _, err := k.hardwareResources(map[string]any{"devices": bad}); err == nil {
		t.Fatal("undeclared Kubernetes device was accepted")
	}
	wrongKind := []any{map[string]any{"id": "fpga-0", "kind": "gpu"}}
	if _, err := k.hardwareResources(map[string]any{"devices": wrongKind}); err == nil {
		t.Fatal("mismatched device kind was accepted")
	}
	duplicates := []any{map[string]any{"id": "gpu-0", "kind": "gpu"},
		map[string]any{"id": "gpu-0", "kind": "gpu"}}
	if _, err := k.hardwareResources(map[string]any{"devices": duplicates}); err == nil {
		t.Fatal("one Kubernetes device slot was allocated twice")
	}
}

func TestKubernetesDeviceManifestRejectsHostMappingAndSpoofing(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "devices.json")
	invalid := []string{
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu","paths":["/dev/null"]}]}`,
		`{"devices":[{"id":"usb","kind":"usb","resource":"example.com/usb"}]}`,
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"kubernetes.io/pods"}]}`,
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu"},{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu"}]}`,
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu"},{"id":"fpga","kind":"fpga","resource":"nvidia.com/gpu"}]}`,
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu"}]}{"devices":[]}`,
		`{"devices":[{"id":"gpu","kind":"gpu","resource":"nvidia.com/gpu"}]} garbage`,
	}
	for _, body := range invalid {
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadKubernetesDevices(filename); err == nil {
			t.Fatalf("unsafe Kubernetes device manifest accepted: %s", body)
		}
	}
}

func TestKubernetesWorkspaceExecRequiresVerifiedPodOwnership(t *testing.T) {
	k, trace := testKubernetes(t)
	id := strings.Repeat("b", 32)
	if _, err := k.exec(context.Background(), id, "sh", "-c", "id"); err == nil {
		t.Fatal("exec permitted a Pod owned by another workspace")
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "exec pod/pwm-"+id) {
		t.Fatal("workspace executed before Pod ownership was verified")
	}
}

func TestKubernetesTaskJobUsesVerifiedDevicePluginResources(t *testing.T) {
	k, _ := testKubernetes(t)
	k.Devices = []KubernetesDevice{{
		ID: "gpu-one", Kind: "gpu", Resource: "nvidia.com/gpu", MemoryMB: 12288,
	}}
	job := Job{
		ID: strings.Repeat("a", 32), Image: "debian:bookworm-slim",
		CPU: 1, MemoryMB: 1024, Command: "echo tested",
		Devices: []DeviceClaim{{ID: "gpu-one", Kind: "gpu"}},
	}
	manifest, err := k.jobManifestWithDevices(job, "worker-one")
	if err != nil {
		t.Fatal(err)
	}
	podSpec := kubeObject(kubeObject(kubeObject(manifest, "spec"), "template"), "spec")
	containers := podSpec["containers"].([]any)
	limits := kubeObject(kubeObject(containers[0].(map[string]any), "resources"), "limits")
	if limits["nvidia.com/gpu"] != "1" {
		t.Fatalf("task GPU allocation missing: %v", limits)
	}
	job.Devices = []DeviceClaim{{ID: "unapproved-gpu", Kind: "gpu"}}
	if _, err := k.jobManifestWithDevices(job, "worker-one"); err == nil {
		t.Fatal("Kubernetes task accepted unapproved GPU device")
	}
}

func TestKubernetesGPURuntimeClass(t *testing.T) {
	k, _ := testKubernetes(t)
	k.GPURuntimeClass = "nvidia-cdi"
	k.Devices = []KubernetesDevice{
		{ID: "gpu-0", Kind: "gpu", Resource: "nvidia.com/gpu"},
		{ID: "npu-0", Kind: "npu", Resource: "npu.intel.com/accel"},
	}
	id := strings.Repeat("a", 32)
	gpuPod := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, map[string]string{"nvidia.com/gpu": "1"})
	if kubeObject(gpuPod, "spec")["runtimeClassName"] != "nvidia-cdi" {
		t.Fatal("GPU workspace does not select the approved runtime class")
	}
	cpuPod := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, nil)
	if kubeObject(cpuPod, "spec")["runtimeClassName"] != nil {
		t.Fatal("CPU workspace unexpectedly selects GPU runtime class")
	}
	npuPod := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, map[string]string{"npu.intel.com/accel": "1"})
	if kubeObject(npuPod, "spec")["runtimeClassName"] != nil {
		t.Fatal("NPU workspace unexpectedly selects GPU runtime class")
	}
	job := Job{ID: id, Image: "debian:bookworm-slim", CPU: 1, MemoryMB: 512, Command: "true", Devices: []DeviceClaim{{ID: "gpu-0", Kind: "gpu"}}}
	manifest, err := k.jobManifestWithDevices(job, "worker-one")
	if err != nil {
		t.Fatal(err)
	}
	jobPod := kubeObject(kubeObject(kubeObject(manifest, "spec"), "template"), "spec")
	if jobPod["runtimeClassName"] != "nvidia-cdi" {
		t.Fatal("GPU job does not select the approved runtime class")
	}
}

func TestKubernetesGPUInvalidRuntimeClass(t *testing.T) {
	_, _ = testKubernetes(t)
	t.Setenv("PWM_KUBE_GPU_RUNTIME_CLASS", "../unsafe")
	if _, err := NewKubernetes("pwm-research", "research", 2, 2048, 1); err == nil {
		t.Fatal("invalid GPU runtime class accepted")
	}
}

func TestNVIDIACDIExposesDriverLibrariesWithoutClobberingImageEnvironment(t *testing.T) {
	k, _ := testKubernetes(t)
	k.GPURuntimeClass = "nvidia-cdi"
	k.Devices = []KubernetesDevice{{ID: "gpu-0", Kind: "gpu", Resource: "nvidia.com/gpu"}}
	id := strings.Repeat("a", 32)
	workspace := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, map[string]string{"nvidia.com/gpu": "1"})
	workspaceSpec := kubeObject(workspace, "spec")
	workspaceContainer := workspaceSpec["containers"].([]any)[0].(map[string]any)
	workspaceCommand := workspaceContainer["command"].([]string)[2]
	for _, wanted := range []string{"/usr/local/nvidia/lib64", "${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"} {
		if !strings.Contains(workspaceCommand, wanted) {
			t.Fatalf("NVIDIA CDI command missing %q: %s", wanted, workspaceCommand)
		}
	}
	cpuPod := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, nil)
	cpuContainer := kubeObject(cpuPod, "spec")["containers"].([]any)[0].(map[string]any)
	if strings.Contains(cpuContainer["command"].([]string)[2], "LD_LIBRARY_PATH") {
		t.Fatal("CPU workload modified by NVIDIA runtime")
	}
	job := Job{ID: id, Image: "debian:bookworm-slim", CPU: 1, MemoryMB: 512, Command: "true", Devices: []DeviceClaim{{ID: "gpu-0", Kind: "gpu"}}}
	jobObject, err := k.jobManifestWithDevices(job, "worker-one")
	if err != nil {
		t.Fatal(err)
	}
	spec := kubeObject(kubeObject(kubeObject(jobObject, "spec"), "template"), "spec")
	jobContainer := spec["containers"].([]any)[0].(map[string]any)
	if !strings.Contains(jobContainer["command"].([]string)[2], "/usr/local/nvidia/lib64") {
		t.Fatal("NVIDIA CDI driver libraries unavailable to GPU batch jobs")
	}
}

func TestNVIDIACDIWorkspaceCommandPreservesDriverLibraries(t *testing.T) {
	k, _ := testKubernetes(t)
	k.GPURuntimeClass = "nvidia-cdi"
	k.Devices = []KubernetesDevice{{ID: "gpu-0", Kind: "gpu", Resource: "nvidia.com/gpu"}}
	id := strings.Repeat("a", 32)
	cudaPod := k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, map[string]string{"nvidia.com/gpu": "1"})
	command := k.prepareWorkspaceCommand(cudaPod, "/workspace/cuda-smoke")
	if !strings.Contains(command, "/usr/local/nvidia/lib64") ||
		!strings.Contains(command, "${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}") {
		t.Fatalf("CUDA driver libraries absent from interactive command: %s", command)
	}
	if strings.Contains(k.prepareWorkspaceCommand(
		k.workspacePod(id, "debian:bookworm-slim", 1, 512, false, 0, 0, nil),
		"echo cpu"), "LD_LIBRARY_PATH") {
		t.Fatal("CPU interactive command received GPU runtime configuration")
	}
}
