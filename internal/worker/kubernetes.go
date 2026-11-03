package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var kubernetesName = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")

// Kubernetes executes workspace Pods, persistent claims, and batch Jobs via
// the Kubernetes API. A worker serves one dedicated project namespace.
type Kubernetes struct {
	Namespace    string
	Project      string
	Worker       string
	Binary       string
	Storage      string
	StorageClass string
	CPU          float64
	MemoryMB     int
	MaxJobs      int
}

func NewKubernetes(namespace, project string, cpu float64, memory, jobs int) (*Kubernetes, error) {
	if !kubernetesName.MatchString(namespace) || !kubernetesName.MatchString(project) ||
		cpu <= 0 || cpu > 1024 || memory < 64 || memory > 1048576 ||
		jobs < 1 || jobs > 256 {
		return nil, errors.New("invalid Kubernetes worker namespace, project, or capacity")
	}
	binary, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, fmt.Errorf("Kubernetes requires kubectl and authorized API access: %w", err)
	}
	storage := os.Getenv("PWM_KUBE_WORKSPACE_STORAGE")
	if storage == "" {
		storage = "10Gi"
	}
	if !regexp.MustCompile("^[1-9][0-9]{0,5}(Mi|Gi|Ti)$").MatchString(storage) {
		return nil, errors.New("invalid Kubernetes workspace storage request")
	}
	class := os.Getenv("PWM_KUBE_STORAGE_CLASS")
	if class != "" && !kubernetesName.MatchString(class) {
		return nil, errors.New("invalid Kubernetes storage class")
	}
	return &Kubernetes{Namespace: namespace, Project: project, Binary: binary,
		Storage: storage, StorageClass: class, CPU: cpu, MemoryMB: memory, MaxJobs: jobs}, nil
}

func (k *Kubernetes) labels(id, kind string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "project-workspace",
		"pwm.dev/kind": kind, "pwm.dev/project": k.Project,
		"pwm.dev/worker": k.Worker, "pwm.dev/workspace": id,
	}
}

func (k *Kubernetes) call(ctx context.Context, limit int64, input io.Reader, args ...string) ([]byte, []byte, int, error) {
	if k == nil || !kubernetesName.MatchString(k.Namespace) {
		return nil, nil, 0, errors.New("Kubernetes worker not configured")
	}
	command := exec.CommandContext(ctx, k.Binary, append([]string{"--namespace", k.Namespace}, args...)...)
	command.Stdin = input
	var out, stderr bytes.Buffer
	command.Stdout = &maxWriter{Writer: &out, Limit: limit}
	command.Stderr = &limitWriter{Writer: &stderr, Limit: 4096}
	err := command.Run()
	if ctx.Err() != nil {
		return out.Bytes(), stderr.Bytes(), -1, ctx.Err()
	}
	if err == nil {
		return out.Bytes(), stderr.Bytes(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), stderr.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), stderr.Bytes(), -1, err
}

func (k *Kubernetes) checked(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, stderr, status, err := k.call(commandCtx, 2<<20, input, args...)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("Kubernetes API operation failed: %s", strings.TrimSpace(string(stderr)))
	}
	return out, nil
}

func (k *Kubernetes) create(ctx context.Context, resource map[string]any) error {
	manifest, err := json.Marshal(resource)
	if err != nil {
		return err
	}
	_, err = k.checked(ctx, bytes.NewReader(manifest), "create", "-f", "-")
	return err
}

func (k *Kubernetes) get(ctx context.Context, kind, name string) (map[string]any, error) {
	out, err := k.checked(ctx, nil, "get", kind, name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func kubeObject(m map[string]any, key string) map[string]any {
	out, _ := m[key].(map[string]any)
	return out
}

func (k *Kubernetes) owns(m map[string]any, id, kind string) bool {
	meta := kubeObject(m, "metadata")
	labels := kubeObject(meta, "labels")
	return labels["app.kubernetes.io/managed-by"] == "project-workspace" &&
		labels["pwm.dev/project"] == k.Project &&
		labels["pwm.dev/worker"] == k.Worker &&
		labels["pwm.dev/workspace"] == id &&
		labels["pwm.dev/kind"] == kind
}

func kubePodName(id string) string { return "pwm-" + id }
func kubeClaimName(id string) string { return "pwm-data-" + id }
func kubeJobName(id string) string { return "pwm-job-" + id }

func kubernetesResources(cpu float64, memory int, devices map[string]string) map[string]any {
	cpuUnits := strconv.FormatInt(int64(cpu*1000+0.5), 10) + "m"
	memoryUnits := strconv.Itoa(memory) + "Mi"
	resources := map[string]any{"cpu": cpuUnits, "memory": memoryUnits}
	for resource, amount := range devices {
		resources[resource] = amount
	}
	return map[string]any{"requests": resources, "limits": resources}
}

func restrictedSecurity() map[string]any {
	return map[string]any{
		"runAsNonRoot": true, "runAsUser": 65534, "runAsGroup": 65534,
		"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
		"capabilities": map[string]any{"drop": []string{"ALL"}},
		"seccompProfile": map[string]any{"type": "RuntimeDefault"},
	}
}

func sandboxPodSecurity() map[string]any {
	return map[string]any{"runAsNonRoot": true, "runAsUser": 65534,
		"runAsGroup": 65534, "fsGroup": 65534,
		"seccompProfile": map[string]any{"type": "RuntimeDefault"}}
}

func (k *Kubernetes) workspacePod(id, image string, cpu float64, memory int, display bool, width, height int, devices map[string]string) map[string]any {
	command := "exec sleep 2147483647"
	env := []map[string]string{{"name": "HOME", "value": "/workspace"}}
	if display {
		command = fmt.Sprintf("Xvfb :99 -screen 0 %dx%dx24 -nolisten tcp & exec sleep 2147483647", width, height)
		env = append(env, map[string]string{"name": "DISPLAY", "value": ":99"},
			map[string]string{"name": "XDG_CACHE_HOME", "value": "/workspace/.cache"},
			map[string]string{"name": "XDG_RUNTIME_DIR", "value": "/tmp"})
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": kubePodName(id), "labels": k.labels(id, "workspace")},
		"spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false,
			"serviceAccountName": "pwm-sandbox",
			"securityContext": sandboxPodSecurity(),
			"containers": []any{map[string]any{
				"name": "workspace", "image": image,
				"imagePullPolicy": "IfNotPresent",
				"command": []string{"sh", "-c", command},
				"workingDir": "/workspace", "env": env,
				"resources": kubernetesResources(cpu, memory, devices),
				"securityContext": restrictedSecurity(),
				"volumeMounts": []any{
					map[string]any{"name": "workspace", "mountPath": "/workspace"},
					map[string]any{"name": "scratch", "mountPath": "/tmp"},
				},
			}},
			"volumes": []any{
				map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": kubeClaimName(id)}},
				map[string]any{"name": "scratch", "emptyDir": map[string]any{"sizeLimit": "64Mi"}},
			},
		},
	}
}

func (k *Kubernetes) claim(id string) map[string]any {
	spec := map[string]any{
		"accessModes": []string{"ReadWriteOnce"},
		"resources": map[string]any{"requests": map[string]string{"storage": k.Storage}},
	}
	if k.StorageClass != "" {
		spec["storageClassName"] = k.StorageClass
	}
	return map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": kubeClaimName(id), "labels": k.labels(id, "volume")},
		"spec": spec}
}

func (k *Kubernetes) Start(ctx context.Context, r *Runner, id string, p map[string]any) (map[string]any, error) {
	if !workspaceID.MatchString(id) || !kubernetesName.MatchString(k.Worker) {
		return nil, errors.New("invalid Kubernetes worker or workspace ID")
	}
	image := getString(p, "image")
	if !r.Images[image] && !imageName.MatchString(image) {
		return nil, errors.New("image not approved")
	}
	cpu, memory := getFloat(p, "cpu"), getFloat(p, "memory_mb")
	if cpu < 0.1 || cpu > 32 || memory < 64 || memory > 131072 || float64(int(memory)) != memory {
		return nil, errors.New("invalid Kubernetes resource request")
	}
	display := p["display"] == true
	width, height := int(getFloat(p, "display_width")), int(getFloat(p, "display_height"))
	if display && !validDisplaySize(width, height) {
		return nil, errors.New("invalid virtual display")
	}
	devices, err := k.hardwareResources(p)
	if err != nil {
		return nil, err
	}
	if err := k.create(ctx, k.claim(id)); err != nil {
		return nil, fmt.Errorf("create workspace PVC: %w", err)
	}
	if err := k.create(ctx, k.workspacePod(id, image, cpu, int(memory), display, width, height, devices)); err != nil {
		return nil, fmt.Errorf("create workspace Pod: %w", err)
	}
	if _, err := k.checked(ctx, nil, "wait", "--for=condition=Ready", "pod/"+kubePodName(id), "--timeout=180s"); err != nil {
		return nil, fmt.Errorf("Kubernetes scheduler did not confirm a ready workspace Pod: %w", err)
	}
	out, err := k.exec(ctx, id, "sh", "-c", "touch /workspace/.pwm-writecheck && rm /workspace/.pwm-writecheck")
	if err != nil || len(out) > 0 {
		return nil, fmt.Errorf("Kubernetes workspace persistent volume is not writable: %w", err)
	}
	if display {
		if _, err := k.exec(ctx, id, "xdpyinfo", "-display", ":99"); err != nil {
			return nil, fmt.Errorf("Kubernetes virtual display unavailable: %w", err)
		}
	}
	return map[string]any{"status": "ready", "pod": kubePodName(id),
		"claim": kubeClaimName(id), "namespace": k.Namespace}, nil
}

// hardwareResources translates leased device-plugin resources into Kubernetes
// extended resources. Host-device paths and direct USB mapping are forbidden.
func (k *Kubernetes) hardwareResources(p map[string]any) (map[string]string, error) {
	var selected []DeviceClaim
	if p["devices"] != nil {
		encoded, _ := json.Marshal(p["devices"])
		if err := json.Unmarshal(encoded, &selected); err != nil {
			return nil, err
		}
	}
	if len(selected) > 8 || p["usb_passthrough"] == true {
		return nil, errors.New("Kubernetes USB passthrough requires a separately approved device-plugin adapter")
	}
	resources := map[string]string{}
	counts := map[string]int{}
	for _, item := range selected {
		if !deviceID.MatchString(item.ID) || item.Kind == "usb" ||
			(item.Kind != "gpu" && item.Kind != "npu" && item.Kind != "fpga") {
			return nil, errors.New("invalid Kubernetes device lease")
		}
		counts[item.Kind]++
	}
	for kind, count := range counts {
		resource := os.Getenv("PWM_KUBE_RESOURCE_" + strings.ToUpper(kind))
		if !regexp.MustCompile("^[a-z0-9.-]+/[A-Za-z0-9_.-]+$").MatchString(resource) {
			return nil, fmt.Errorf("Kubernetes %s device-plugin resource is not configured", kind)
		}
		resources[resource] = strconv.Itoa(count)
	}
	return resources, nil
}

func (k *Kubernetes) exec(ctx context.Context, id string, args ...string) ([]byte, error) {
	if !workspaceID.MatchString(id) {
		return nil, errors.New("invalid workspace ID")
	}
	command := append([]string{"exec", "pod/" + kubePodName(id), "-c", "workspace", "--"}, args...)
	return k.checked(ctx, nil, command...)
}

func (k *Kubernetes) Command(ctx context.Context, id, command string, input io.Reader) (map[string]any, error) {
	if !workspaceID.MatchString(id) || strings.TrimSpace(command) == "" || len(command) > 16000 {
		return nil, errors.New("invalid workspace command")
	}
	args := []string{"exec"}
	if input != nil {
		args = append(args, "-i")
	}
	args = append(args, "pod/"+kubePodName(id), "-c", "workspace", "--", "sh", "-c", command)
	commandCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, stderr, status, err := k.call(commandCtx, 2<<20, input, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stdout": string(out), "stderr": string(stderr), "exit_code": status}, nil
}

func (k *Kubernetes) Stop(ctx context.Context, id string) (map[string]any, error) {
	if !workspaceID.MatchString(id) {
		return nil, errors.New("invalid workspace ID")
	}
	pod, err := k.get(ctx, "pod", kubePodName(id))
	if err != nil {
		return nil, fmt.Errorf("inspect workspace Pod before termination: %w", err)
	}
	if !k.owns(pod, id, "workspace") {
		return nil, errors.New("workspace Pod does not match controller ownership")
	}
	if _, err := k.checked(ctx, nil, "delete", "pod", kubePodName(id),
		"--cascade=foreground", "--wait=true", "--timeout=90s"); err != nil {
		return nil, err
	}
	return map[string]any{"status": "stopped", "claim": kubeClaimName(id)}, nil
}

func (k *Kubernetes) PruneVolume(ctx context.Context, id string) (map[string]any, error) {
	if !workspaceID.MatchString(id) {
		return nil, errors.New("invalid workspace ID")
	}
	claim, err := k.get(ctx, "pvc", kubeClaimName(id))
	if err != nil || !k.owns(claim, id, "volume") {
		return nil, errors.New("persistent claim is absent or does not match workspace ownership")
	}
	pods, err := k.checked(ctx, nil, "get", "pods", "-l", "pwm.dev/workspace="+id, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list map[string]any
	if err := json.Unmarshal(pods, &list); err != nil {
		return nil, err
	}
	if items, _ := list["items"].([]any); len(items) != 0 {
		return nil, errors.New("workspace claim remains attached to a Pod")
	}
	if _, err := k.checked(ctx, nil, "delete", "pvc", kubeClaimName(id), "--wait=true", "--timeout=90s"); err != nil {
		return nil, err
	}
	return map[string]any{"status": "pruned", "claim": kubeClaimName(id)}, nil
}

func (k *Kubernetes) jobManifest(job Job, worker string) map[string]any {
	name := kubeJobName(job.ID)
	return map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "labels": map[string]string{
			"app.kubernetes.io/managed-by": "project-workspace", "pwm.dev/kind": "job",
			"pwm.dev/worker": worker}},
		"spec": map[string]any{
			"backoffLimit": 0, "activeDeadlineSeconds": 330, "ttlSecondsAfterFinished": 3600,
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]string{
				"app.kubernetes.io/managed-by": "project-workspace", "pwm.dev/kind": "job", "pwm.dev/worker": worker,
			}}, "spec": map[string]any{
				"restartPolicy": "Never", "automountServiceAccountToken": false,
				"serviceAccountName": "pwm-sandbox", "securityContext": sandboxPodSecurity(),
				"containers": []any{map[string]any{
					"name": "task", "image": job.Image, "command": []string{"sh", "-c", job.Command},
					"resources": kubernetesResources(job.CPU, job.MemoryMB, nil),
					"securityContext": restrictedSecurity(),
					"volumeMounts": []any{map[string]any{"name": "scratch", "mountPath": "/tmp"}},
				}},
				"volumes": []any{map[string]any{"name": "scratch", "emptyDir": map[string]any{"sizeLimit": "64Mi"}}},
			}},
		},
	}
}

func (k *Kubernetes) ExecuteJob(ctx context.Context, r *Runner, job Job) (string, string) {
	if !workspaceID.MatchString(job.ID) || !r.Images[job.Image] || job.CPU < 0.1 ||
		job.CPU > 32 || job.MemoryMB < 64 || job.MemoryMB > 131072 {
		return "failed", "invalid or unapproved Kubernetes job"
	}
	if !kubernetesName.MatchString(k.Worker) {
		return "failed", "invalid Kubernetes worker identity"
	}
	if err := k.create(ctx, k.jobManifest(job, k.Worker)); err != nil {
		return "failed", fmt.Sprintf("create Kubernetes Job: %v", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = k.checked(cleanupCtx, nil, "delete", "job", kubeJobName(job.ID),
			"--cascade=foreground", "--wait=true", "--timeout=25s")
	}()
	deadline, cancel := context.WithTimeout(ctx, 330*time.Second)
	defer cancel()
	for {
		item, err := k.get(deadline, "job", kubeJobName(job.ID))
		if err != nil {
			return "failed", fmt.Sprintf("inspect Kubernetes Job: %v", err)
		}
		status := kubeObject(item, "status")
		if status["succeeded"] == float64(1) {
			out, err := k.checked(deadline, nil, "logs", "job/"+kubeJobName(job.ID), "--all-containers=true")
			if err != nil {
				return "failed", fmt.Sprintf("read Kubernetes Job logs: %v", err)
			}
			if len(out) > 32768 {
				out = out[len(out)-32768:]
			}
			return "succeeded", string(out)
		}
		if failed, _ := status["failed"].(float64); failed > 0 {
			out, _ := k.checked(deadline, nil, "logs", "job/"+kubeJobName(job.ID), "--all-containers=true")
			if len(out) > 32768 {
				out = out[len(out)-32768:]
			}
			return "failed", string(out)
		}
		select {
		case <-deadline.Done():
			return "failed", deadline.Err().Error()
		case <-time.After(2 * time.Second):
		}
	}
}

func (k *Kubernetes) ReconcileStaleJobs(ctx context.Context) error {
	if !kubernetesName.MatchString(k.Worker) {
		return errors.New("invalid worker name")
	}
	raw, err := k.checked(ctx, nil, "get", "jobs", "-l",
		"app.kubernetes.io/managed-by=project-workspace,pwm.dev/kind=job,pwm.dev/worker="+k.Worker, "-o", "json")
	if err != nil {
		return err
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	list, _ := response["items"].([]any)
	if len(list) > 128 {
		return errors.New("too many abandoned Kubernetes Jobs")
	}
	for _, item := range list {
		job, _ := item.(map[string]any)
		meta := kubeObject(job, "metadata")
		name, _ := meta["name"].(string)
		if !strings.HasPrefix(name, "pwm-job-") || !workspaceID.MatchString(strings.TrimPrefix(name, "pwm-job-")) {
			return errors.New("unexpected Kubernetes Job in reconciliation inventory")
		}
		labels := kubeObject(meta, "labels")
		if labels["pwm.dev/worker"] != k.Worker || labels["pwm.dev/kind"] != "job" {
			return errors.New("Kubernetes Job ownership mismatch")
		}
		if _, err := k.checked(ctx, nil, "delete", "job", name, "--cascade=foreground", "--wait=true"); err != nil {
			return err
		}
	}
	return nil
}
