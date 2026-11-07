package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var kubernetesName = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")

// A project worker must not operate against infrastructure namespaces, even
// if invoked outside the Helm installation path.
func protectedKubernetesNamespace(name string) bool {
	switch name {
	case "default", "kube-system", "kube-public", "kube-node-lease", "pwm-system":
		return true
	}
	return false
}

// StorageClass names use DNS subdomains and may contain several labels.
func validKubernetesStorageClass(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !kubernetesName.MatchString(label) {
			return false
		}
	}
	return true
}

// Kubernetes executes workspace Pods, persistent claims, and batch Jobs via
// the Kubernetes API. A worker serves one dedicated project namespace.
type Kubernetes struct {
	Namespace       string
	Project         string
	Worker          string
	Binary          string
	ConfigPath      string
	OwnsConfig      bool
	Storage         string
	StorageClass    string
	ImagePullSecret string
	GPURuntimeClass string
	WorkloadNode    string
	Devices         []KubernetesDevice
	CPU             float64
	MemoryMB        int
	MaxJobs         int
}

func NewKubernetes(namespace, project string, cpu float64, memory, jobs int) (*Kubernetes, error) {
	if !kubernetesName.MatchString(namespace) || protectedKubernetesNamespace(namespace) ||
		!kubernetesName.MatchString(project) ||
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
	if class != "" && !validKubernetesStorageClass(class) {
		return nil, errors.New("invalid Kubernetes storage class")
	}
	pullSecret := os.Getenv("PWM_KUBE_IMAGE_PULL_SECRET")
	if pullSecret != "" && !kubernetesName.MatchString(pullSecret) {
		return nil, errors.New("invalid Kubernetes workload registry pull secret")
	}
	workloadNode := os.Getenv("PWM_KUBE_WORKLOAD_NODE")
	if workloadNode != "" && !kubernetesName.MatchString(workloadNode) {
		return nil, errors.New("invalid Kubernetes workload node")
	}
	runtimeClass := os.Getenv("PWM_KUBE_GPU_RUNTIME_CLASS")
	if runtimeClass != "" && !kubernetesName.MatchString(runtimeClass) {
		return nil, errors.New("invalid Kubernetes GPU runtime class")
	}
	k := &Kubernetes{Namespace: namespace, Project: project, Binary: binary,
		Storage: storage, StorageClass: class, ImagePullSecret: pullSecret,
		WorkloadNode: workloadNode, CPU: cpu, MemoryMB: memory, MaxJobs: jobs}
	k.GPURuntimeClass = runtimeClass
	if err := k.configureServiceAccount(); err != nil {
		return nil, err
	}
	return k, nil
}

// configureServiceAccount creates an ephemeral, owner-only kubeconfig.
// kubectl does not receive the API bearer token as a command-line argument.
func (k *Kubernetes) configureServiceAccount() error {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return nil // Operator-provided kubeconfig for external cluster access.
	}
	port := os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
	if port == "" {
		port = os.Getenv("KUBERNETES_SERVICE_PORT")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return errors.New("invalid Kubernetes API service port")
	}
	root := "/var/run/secrets/kubernetes.io/serviceaccount"
	token, err := os.ReadFile(root + "/token")
	if err != nil {
		return fmt.Errorf("worker ServiceAccount token not mounted: %w", err)
	}
	credential := strings.TrimSpace(string(token))
	if len(credential) < 20 || len(credential) > 16384 || strings.ContainsAny(credential, "\x00\r\n ") {
		return errors.New("invalid Kubernetes ServiceAccount credential")
	}
	if _, err := os.Stat(root + "/ca.crt"); err != nil {
		return fmt.Errorf("Kubernetes API CA unavailable: %w", err)
	}
	config := map[string]any{
		"apiVersion": "v1", "kind": "Config",
		"clusters": []any{map[string]any{"name": "cluster",
			"cluster": map[string]any{
				"server":                "https://" + net.JoinHostPort(host, port),
				"certificate-authority": root + "/ca.crt",
			}}},
		"contexts": []any{map[string]any{"name": "worker",
			"context": map[string]string{"cluster": "cluster", "user": "worker", "namespace": k.Namespace}}},
		"current-context": "worker",
		"users": []any{map[string]any{"name": "worker",
			"user": map[string]string{"token": credential}}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "pwm-kubeconfig-*")
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		_ = os.Remove(file.Name())
		return err
	}
	k.ConfigPath = file.Name()
	k.OwnsConfig = true
	return nil
}

func (k *Kubernetes) baseArgs() []string {
	args := []string{}
	if k.ConfigPath != "" {
		args = append(args, "--kubeconfig", k.ConfigPath)
	}
	return append(args, "--namespace", k.Namespace)
}

func (k *Kubernetes) Close() {
	if k != nil && k.OwnsConfig {
		_ = os.Remove(k.ConfigPath)
		k.ConfigPath = ""
		k.OwnsConfig = false
	}
}

func (k *Kubernetes) labels(id, kind string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "project-workspace",
		"pwm.dev/kind":                 kind, "pwm.dev/project": k.Project,
		"pwm.dev/worker": k.Worker, "pwm.dev/workspace": id,
	}
}

func (k *Kubernetes) call(ctx context.Context, limit int64, input io.Reader, args ...string) ([]byte, []byte, int, error) {
	if k == nil || !kubernetesName.MatchString(k.Namespace) {
		return nil, nil, 0, errors.New("Kubernetes worker not configured")
	}
	command := exec.CommandContext(ctx, k.Binary, append(k.baseArgs(), args...)...)
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

// ensureOwnedCreate permits an interrupted start operation to be retried without
// overwriting an existing Pod or claim. The canonical specification fingerprint
// must match; Kubernetes adds default fields to live Pod specs.
func (k *Kubernetes) ensureOwnedCreate(ctx context.Context, resource map[string]any, kind, name, id, ownerKind string) error {
	spec, ok := resource["spec"].(map[string]any)
	if !ok {
		return errors.New("Kubernetes resource has no specification")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(hash[:])
	meta := kubeObject(resource, "metadata")
	meta["annotations"] = map[string]string{"pwm.dev/spec-sha256": fingerprint}
	if err := k.create(ctx, resource); err == nil {
		return nil
	} else {
		live, lookupErr := k.get(ctx, kind, name)
		if lookupErr != nil {
			return fmt.Errorf("create Kubernetes %s %s: %w (lookup: %v)", kind, name, err, lookupErr)
		}
		if !k.owns(live, id, ownerKind) {
			return fmt.Errorf("existing Kubernetes %s is not owned by this workspace", kind)
		}
		annotations := kubeObject(kubeObject(live, "metadata"), "annotations")
		if annotations["pwm.dev/spec-sha256"] != fingerprint {
			return fmt.Errorf("existing Kubernetes %s specification does not match the requested workspace", kind)
		}
		return nil
	}
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
	if typed, ok := meta["labels"].(map[string]string); ok {
		labels = make(map[string]any, len(typed))
		for key, value := range typed {
			labels[key] = value
		}
	}
	return labels["app.kubernetes.io/managed-by"] == "project-workspace" &&
		labels["pwm.dev/project"] == k.Project &&
		labels["pwm.dev/worker"] == k.Worker &&
		labels["pwm.dev/workspace"] == id &&
		labels["pwm.dev/kind"] == kind
}

func kubePodName(id string) string   { return "pwm-" + id }
func kubeClaimName(id string) string { return "pwm-data-" + id }
func kubeJobName(id string) string   { return "pwm-job-" + id }

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
		"capabilities":   map[string]any{"drop": []string{"ALL"}},
		"seccompProfile": map[string]any{"type": "RuntimeDefault"},
	}
}

func sandboxPodSecurity() map[string]any {
	return map[string]any{"runAsNonRoot": true, "runAsUser": 65534,
		"runAsGroup": 65534, "fsGroup": 65534,
		"seccompProfile": map[string]any{"type": "RuntimeDefault"}}
}

const nvidiaLibrarySetup = `export LD_LIBRARY_PATH="/usr/local/nvidia/lib64:/run/opengl-driver/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"; `

// addGPURuntimeClass selects an operator-configured device-injection runtime
// only for Pods with an approved GPU device-plugin resource.
func (k *Kubernetes) addGPURuntimeClass(spec map[string]any, resources map[string]string) {
	if k.GPURuntimeClass == "" {
		return
	}
	for _, device := range k.Devices {
		if device.Kind == "gpu" && resources[device.Resource] != "" {
			spec["runtimeClassName"] = k.GPURuntimeClass
			if k.GPURuntimeClass == "nvidia-cdi" {
				// CDI exposes NVIDIA libraries under the injected driver paths.
				// Preserve the image's existing LD_LIBRARY_PATH.

				containers, _ := spec["containers"].([]any)
				for _, raw := range containers {
					container, _ := raw.(map[string]any)
					command, _ := container["command"].([]string)
					if len(command) == 3 && command[0] == "sh" && command[1] == "-c" {
						command[2] = nvidiaLibrarySetup + command[2]
						container["command"] = command
					}
				}
			}
			return
		}
	}
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
	result := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": kubePodName(id), "labels": k.labels(id, "workspace")},
		"spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false,
			"serviceAccountName": "pwm-sandbox",
			"securityContext":    sandboxPodSecurity(),
			"containers": []any{map[string]any{
				"name": "workspace", "image": image,
				"imagePullPolicy": "IfNotPresent",
				"command":         []string{"sh", "-c", command},
				"workingDir":      "/workspace", "env": env,
				"resources":       kubernetesResources(cpu, memory, devices),
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
	if k.WorkloadNode != "" {
		kubeObject(result, "spec")["nodeSelector"] = map[string]any{"kubernetes.io/hostname": k.WorkloadNode}
	}
	k.addGPURuntimeClass(kubeObject(result, "spec"), devices)
	if k.ImagePullSecret != "" {
		kubeObject(result, "spec")["imagePullSecrets"] =
			[]any{map[string]string{"name": k.ImagePullSecret}}
	}
	return result
}
func (k *Kubernetes) claim(id string) map[string]any {
	spec := map[string]any{
		"accessModes": []string{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]string{"storage": k.Storage}},
	}
	if k.StorageClass != "" {
		spec["storageClassName"] = k.StorageClass
	}
	return map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": kubeClaimName(id), "labels": k.labels(id, "volume")},
		"spec":     spec}
}

func (k *Kubernetes) Start(ctx context.Context, r *Runner, id string, p map[string]any) (map[string]any, error) {
	if !workspaceID.MatchString(id) || !kubernetesName.MatchString(k.Worker) {
		return nil, errors.New("invalid Kubernetes worker or workspace ID")
	}
	image := getString(p, "image")
	if !r.Images[image] {
		return nil, errors.New("Kubernetes Pod image is not in the approved OCI registry pool")
	}
	cpu, memory := getFloat(p, "cpu"), getFloat(p, "memory_mb")
	if math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0.1 || cpu > 32 || cpu > k.CPU ||
		memory < 64 || memory > 131072 || memory > float64(k.MemoryMB) ||
		float64(int(memory)) != memory {
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
	if err := k.ensureOwnedCreate(ctx, k.claim(id), "pvc", kubeClaimName(id), id, "volume"); err != nil {
		return nil, fmt.Errorf("create workspace PVC: %w", err)
	}
	if err := k.ensureOwnedCreate(ctx, k.workspacePod(id, image, cpu, int(memory), display, width, height, devices),
		"pod", kubePodName(id), id, "workspace"); err != nil {
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
	if p["usb_passthrough"] == true {
		return nil, errors.New("USB passthrough requires a separately approved Kubernetes device-plugin adapter")
	}
	var selected []DeviceClaim
	if p["devices"] != nil {
		data, err := json.Marshal(p["devices"])
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &selected); err != nil {
			return nil, err
		}
	}
	if len(selected) > 8 {
		return nil, errors.New("too many Kubernetes device allocations")
	}
	approved := make(map[string]KubernetesDevice, len(k.Devices))
	for _, device := range k.Devices {
		approved[device.ID] = device
	}
	resources := map[string]string{}
	counts := map[string]int{}
	used := map[string]bool{}
	for _, lease := range selected {
		device, ok := approved[lease.ID]
		if !ok || used[lease.ID] || device.Kind != lease.Kind || device.Resource == "" {
			return nil, errors.New("hardware lease does not match Kubernetes device-plugin policy")
		}
		used[lease.ID] = true
		counts[device.Resource]++
	}
	for resource, count := range counts {
		resources[resource] = strconv.Itoa(count)
	}
	return resources, nil
}

// requireWorkspace checks project and worker ownership before any Pod exec.
func (k *Kubernetes) inspectWorkspace(ctx context.Context, id string) (map[string]any, error) {
	if !workspaceID.MatchString(id) {
		return nil, errors.New("invalid workspace ID")
	}
	pod, err := k.get(ctx, "pod", kubePodName(id))
	if err != nil {
		return nil, fmt.Errorf("inspect workspace Pod: %w", err)
	}
	if !k.owns(pod, id, "workspace") {
		return nil, errors.New("workspace Pod does not belong to this worker and project")
	}
	if kubeObject(pod, "metadata")["deletionTimestamp"] != nil {
		return nil, errors.New("workspace Pod is terminating")
	}
	return pod, nil
}

func (k *Kubernetes) requireWorkspace(ctx context.Context, id string) error {
	_, err := k.inspectWorkspace(ctx, id)
	return err
}

func (k *Kubernetes) exec(ctx context.Context, id string, args ...string) ([]byte, error) {
	if err := k.requireWorkspace(ctx, id); err != nil {
		return nil, err
	}
	command := append([]string{"exec", "pod/" + kubePodName(id), "-c", "workspace", "--"}, args...)
	return k.checked(ctx, nil, command...)
}

// prepareWorkspaceCommand sets driver search paths for new exec processes;
// shell exports used by Pod PID 1 do not extend to Kubernetes exec sessions.
func (k *Kubernetes) prepareWorkspaceCommand(pod map[string]any, command string) string {
	if k.GPURuntimeClass == "nvidia-cdi" &&
		kubeObject(pod, "spec")["runtimeClassName"] == "nvidia-cdi" {
		return nvidiaLibrarySetup + command
	}
	return command
}

func (k *Kubernetes) Command(ctx context.Context, id, command string, input io.Reader) (map[string]any, error) {
	if !workspaceID.MatchString(id) || strings.TrimSpace(command) == "" || len(command) > 16000 {
		return nil, errors.New("invalid workspace command")
	}
	pod, err := k.inspectWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}
	command = k.prepareWorkspaceCommand(pod, command)
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
		if !strings.Contains(err.Error(), "(NotFound)") {
			return nil, fmt.Errorf("inspect workspace Pod before termination: %w", err)
		}
		claim, claimErr := k.get(ctx, "pvc", kubeClaimName(id))
		if claimErr != nil || !k.owns(claim, id, "volume") {
			return nil, errors.New("workspace Pod is absent and retained claim could not be verified")
		}
		return map[string]any{"status": "stopped", "claim": kubeClaimName(id)}, nil
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
	result := map[string]any{
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
					"resources":       kubernetesResources(job.CPU, job.MemoryMB, nil),
					"securityContext": restrictedSecurity(),
					"volumeMounts":    []any{map[string]any{"name": "scratch", "mountPath": "/tmp"}},
				}},
				"volumes": []any{map[string]any{"name": "scratch", "emptyDir": map[string]any{"sizeLimit": "64Mi"}}},
			}},
		},
	}
	if k.WorkloadNode != "" {
		pod := kubeObject(kubeObject(kubeObject(result, "spec"), "template"), "spec")
		pod["nodeSelector"] = map[string]any{"kubernetes.io/hostname": k.WorkloadNode}
	}
	if k.ImagePullSecret != "" {
		jobTemplate := kubeObject(kubeObject(result, "spec"), "template")
		kubeObject(jobTemplate, "spec")["imagePullSecrets"] =
			[]any{map[string]string{"name": k.ImagePullSecret}}
	}
	return result
}

// jobManifestWithDevices asks Kubernetes to allocate only operator-approved
// extended resources named by a controller-issued device lease.
func (k *Kubernetes) jobManifestWithDevices(job Job, worker string) (map[string]any, error) {
	resources, err := k.hardwareResources(map[string]any{"devices": job.Devices})
	if err != nil {
		return nil, err
	}
	manifest := k.jobManifest(job, worker)
	podSpec := kubeObject(kubeObject(kubeObject(manifest, "spec"), "template"), "spec")
	containers, ok := podSpec["containers"].([]any)
	if !ok || len(containers) != 1 {
		return nil, errors.New("invalid Kubernetes task Pod template")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return nil, errors.New("invalid Kubernetes task container")
	}
	container["resources"] = kubernetesResources(job.CPU, job.MemoryMB, resources)
	k.addGPURuntimeClass(podSpec, resources)
	return manifest, nil
}

func (k *Kubernetes) ExecuteJob(ctx context.Context, r *Runner, job Job) (string, string) {
	if !workspaceID.MatchString(job.ID) || !r.Images[job.Image] || job.CPU < 0.1 ||
		job.CPU > 32 || job.CPU > k.CPU || math.IsNaN(job.CPU) || math.IsInf(job.CPU, 0) ||
		job.MemoryMB < 64 || job.MemoryMB > 131072 || job.MemoryMB > k.MemoryMB {
		return "failed", "invalid or unapproved Kubernetes job"
	}
	if !kubernetesName.MatchString(k.Worker) {
		return "failed", "invalid Kubernetes worker identity"
	}
	manifest, err := k.jobManifestWithDevices(job, k.Worker)
	if err != nil {
		return "failed", fmt.Sprintf("invalid task device-plugin lease: %v", err)
	}
	if err := k.create(ctx, manifest); err != nil {
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
