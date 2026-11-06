package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Runner struct {
	Kube               *Kubernetes
	WorkerID           string
	ReconcileOnConnect bool
	Devices            []Device
	USBEnabled         bool
	Images             map[string]bool
	BuildNetwork       bool
	MaxArtifact        int64
}

var workspaceID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var imageName = regexp.MustCompile(`^pwm-env-[a-z][a-z0-9-]+:v[1-9][0-9]*$`)

func NewRunner(devices []Device) *Runner {
	images := map[string]bool{}
	allowed := os.Getenv("PWM_ALLOWED_IMAGES")
	if allowed == "" {
		allowed = "debian:bookworm-slim"
	}
	for _, x := range strings.Split(allowed, ",") {
		if strings.TrimSpace(x) != "" {
			images[strings.TrimSpace(x)] = true
		}
	}
	max := int64(2 * 1024 * 1024 * 1024)
	if v := os.Getenv("PWM_MAX_ARTIFACT_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n <= 100*1024*1024*1024 {
			max = n
		}
	}
	return &Runner{Devices: devices, Images: images, BuildNetwork: os.Getenv("PWM_BUILD_NETWORK") == "1", MaxArtifact: max}
}

// ApprovedImages reports image policy without including credentials or host paths.
func (r *Runner) ApprovedImages() []string {
	images := make([]string, 0, len(r.Images))
	for image := range r.Images {
		images = append(images, image)
	}
	sort.Strings(images)
	return images
}

// AvailableDevices rechecks removable USB disks on each heartbeat and allocation.
func (r *Runner) AvailableDevices() []Device {
	devices := make([]Device, 0, len(r.Devices))
	ids := make(map[string]bool)
	for _, d := range r.Devices {
		if d.Kind == "usb" && !r.USBEnabled {
			continue
		}
		if d.Kind == "usb" {
			if _, _, err := usbDockerMapping(d.Paths[0]); err != nil {
				continue
			}
		}
		devices = append(devices, d)
		ids[d.ID] = true
	}
	if r.USBEnabled {
		for _, d := range DiscoverUSBDisks() {
			if !ids[d.ID] && len(devices) < 64 {
				devices = append(devices, d)
				ids[d.ID] = true
			}
		}
	}
	return devices
}

func name(id string) (string, error) {
	if !workspaceID.MatchString(id) {
		return "", errors.New("invalid workspace ID")
	}
	return "pwm-" + id, nil
}

func (r *Runner) docker(ctx context.Context, timeout time.Duration, input io.Reader, args ...string) ([]byte, []byte, int, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil, -1, errors.New("Docker is not installed")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "docker", args...)
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{Writer: &stdout, Limit: 512 * 1024}
	cmd.Stderr = &limitWriter{Writer: &stderr, Limit: 32 * 1024}
	err := cmd.Run()
	if runCtx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, runCtx.Err()
	}
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	if e, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), e.ExitCode(), nil
	}
	return stdout.Bytes(), stderr.Bytes(), -1, err
}

type limitWriter struct {
	Writer  io.Writer
	Limit   int64
	Written int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	left := w.Limit - w.Written
	if left > 0 {
		copySize := n
		if int64(copySize) > left {
			copySize = int(left)
		}
		_, err := w.Writer.Write(p[:copySize])
		if err != nil {
			return 0, err
		}
		w.Written += int64(copySize)
	}
	return n, nil
}

func (r *Runner) checked(ctx context.Context, timeout time.Duration, input io.Reader, args ...string) ([]byte, error) {
	out, stderr, status, err := r.docker(ctx, timeout, input, args...)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("docker exit %d: %s", status, string(stderr))
	}
	return out, nil
}

func stringPayload(p json.RawMessage) (map[string]any, error) {
	data := map[string]any{}
	if err := json.Unmarshal(p, &data); err != nil {
		return nil, err
	}
	return data, nil
}
func getString(p map[string]any, key string) string { value, _ := p[key].(string); return value }
func getFloat(p map[string]any, key string) float64 { value, _ := p[key].(float64); return value }
func safePath(value string) (string, error) {
	if value == "" || len(value) >= 512 || strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "/") {
		return "", errors.New("invalid workspace path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || part == "" {
			return "", errors.New("invalid workspace path")
		}
	}
	return "/workspace/" + path.Clean(value), nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (r *Runner) Start(ctx context.Context, id string, p map[string]any) (map[string]any, error) {
	n, err := name(id)
	if err != nil {
		return nil, err
	}
	image := getString(p, "image")
	if !r.Images[image] && !imageName.MatchString(image) {
		return nil, errors.New("image is not approved")
	}
	cpu := getFloat(p, "cpu")
	memory := getFloat(p, "memory_mb")
	if cpu < 0.1 || cpu > 32 || memory < 64 || memory > 131072 {
		return nil, errors.New("invalid resource allocation")
	}
	display := p["display"] == true
	width, height := getFloat(p, "display_width"), getFloat(p, "display_height")
	if display {
		if width != float64(int(width)) || height != float64(int(height)) ||
			!validDisplaySize(int(width), int(height)) {
			return nil, errors.New("invalid virtual display size")
		}
	} else if width != 0 || height != 0 {
		return nil, errors.New("virtual display dimensions require display opt-in")
	}
	var selected []DeviceClaim
	if p["devices"] != nil {
		encoded, _ := json.Marshal(p["devices"])
		if err := json.Unmarshal(encoded, &selected); err != nil {
			return nil, err
		}
	}
	for _, d := range selected {
		if d.Kind == "usb" && (!r.USBEnabled || p["usb_passthrough"] != true) {
			return nil, errors.New("USB passthrough requires the worker opt-in and workspace toggle")
		}
	}
	flags, err := DeviceDockerArgsAuthorized(selected, r.AvailableDevices(), r.USBEnabled && p["usb_passthrough"] == true)
	if err != nil {
		return nil, err
	}
	if _, err := r.checked(ctx, 30*time.Second, nil, "volume", "create", "--label", "project-workspace=volume",
		"--label", "project-workspace-workspace="+id, n); err != nil {
		return nil, err
	}
	volume := "type=volume,source=" + n + ",target=/workspace,volume-nocopy"
	init := []string{"run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "CHOWN",
		"--cap-add", "FOWNER", "--user", "0", "--mount", volume, image, "sh", "-c",
		"chown 65534:65534 /workspace && chmod 0700 /workspace"}
	if _, err := r.checked(ctx, 10*time.Minute, nil, init...); err != nil {
		return nil, err
	}
	args := []string{"run", "-d", "--name", n, "--label", "project-workspace=managed",
		"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "128", "--cpus", strconv.FormatFloat(cpu, 'f', 2, 64),
		"--memory", fmt.Sprintf("%dm", int(memory)), "--memory-swap", fmt.Sprintf("%dm", int(memory)),
		"--tmpfs", "/tmp:rw,nosuid,noexec,size=64m"}
	args = append(args, flags...)
	if display {
		args = append(args, "--env", "DISPLAY=:99", "--env", "HOME=/workspace",
			"--env", "XDG_CONFIG_HOME=/workspace/.config",
			"--env", "XDG_CACHE_HOME=/workspace/.cache",
			"--env", "XDG_RUNTIME_DIR=/tmp")
	}
	args = append(args, "--user", "65534:65534", "--workdir", "/workspace", "--mount", volume,
		image, "sh", "-c", "while :; do sleep 3600; done")
	if _, err := r.checked(ctx, 10*time.Minute, nil, args...); err != nil {
		return nil, err
	}
	if _, err := r.checked(ctx, 30*time.Second, nil, "exec", n, "sh", "-c",
		"touch /workspace/.pwm-writecheck && rm /workspace/.pwm-writecheck"); err != nil {
		_, _, _, _ = r.docker(ctx, 30*time.Second, nil, "rm", "-f", n)
		return nil, err
	}
	if display {
		if err := r.startVirtualDisplay(ctx, n, int(width), int(height)); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_, _, _, _ = r.docker(cleanupCtx, 15*time.Second, nil, "rm", "-f", n)
			cancel()
			return nil, fmt.Errorf("virtual display startup failed: %w", err)
		}
		return map[string]any{"status": "ready", "container": n, "display": ":99", "width": width, "height": height}, nil
	}
	return map[string]any{"status": "ready", "container": n}, nil
}

func (r *Runner) Command(ctx context.Context, id, command string, stdin io.Reader) (map[string]any, error) {
	n, err := name(id)
	if err != nil {
		return nil, err
	}
	if command == "" || len(command) > 16000 {
		return nil, errors.New("invalid command")
	}
	args := []string{"exec", n, "sh", "-c", command}
	if stdin != nil {
		args = []string{"exec", "-i", n, "sh", "-c", command}
	}
	out, errout, status, err := r.docker(ctx, 120*time.Second, stdin, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"exit_code": status, "stdout": string(out), "stderr": string(errout)}, nil
}

func (r *Runner) streamDocker(ctx context.Context, input io.Reader, args ...string) (*os.File, string, error) {
	file, err := os.CreateTemp("", "pwm-archive-*")
	if err != nil {
		return nil, "", err
	}
	os.Remove(file.Name()) // anonymous temporary data; never persists in the source tree
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "docker", args...)
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{Writer: &stderr, Limit: 4096}
	h := sha256.New()
	writer := io.MultiWriter(file, h)
	cmd.Stdout = &maxWriter{Writer: writer, Limit: r.MaxArtifact}
	if err := cmd.Run(); err != nil {
		file.Close()
		return nil, "", fmt.Errorf("docker export: %w: %s", err, string(stderr.Bytes()))
	}
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		return nil, "", err
	}
	return file, hex.EncodeToString(h.Sum(nil)), nil
}

type maxWriter struct {
	Writer io.Writer
	Limit  int64
	N      int64
}

func (w *maxWriter) Write(p []byte) (int, error) {
	if int64(len(p))+w.N > w.Limit {
		return 0, errors.New("artifact exceeds worker transfer limit")
	}
	n, err := w.Writer.Write(p)
	w.N += int64(n)
	return n, err
}

func (r *Runner) saveOutput(ctx context.Context, op Operation, c *Client, artifactName string, input io.Reader, args ...string) (map[string]any, error) {
	file, sha, err := r.streamDocker(ctx, input, args...)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	artifactID, err := c.Upload(ctx, op, artifactName, file, sha)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact_id": artifactID}, nil
}

// workspaceExec preserves tar validation and artifact integrity across runtimes.
func (r *Runner) workspaceExec(ctx context.Context, timeout time.Duration, input io.Reader, id string, command ...string) ([]byte, error) {
	n, err := name(id)
	if err != nil {
		return nil, err
	}
	if r.Kube != nil {
		if err := r.Kube.requireWorkspace(ctx, id); err != nil {
			return nil, err
		}
		args := []string{"exec"}
		if input != nil {
			args = append(args, "-i")
		}
		args = append(args, "pod/"+n, "-c", "workspace", "--")
		args = append(args, command...)
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, stderr, status, err := r.Kube.call(runCtx, 2<<20, input, args...)
		if err != nil {
			return nil, err
		}
		if status != 0 {
			return nil, fmt.Errorf("Kubernetes workspace command failed: %s", strings.TrimSpace(string(stderr)))
		}
		return out, nil
	}
	args := []string{"exec"}
	if input != nil {
		args = append(args, "-i")
	}
	args = append(args, n)
	args = append(args, command...)
	return r.checked(ctx, timeout, input, args...)
}

func (r *Runner) restore(ctx context.Context, op Operation, c *Client) (map[string]any, error) {
	var payload struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return nil, err
	}
	if !workspaceID.MatchString(payload.ArtifactID) {
		return nil, errors.New("invalid artifact ID")
	}
	temp, err := os.CreateTemp("", "pwm-restore-*")
	if err != nil {
		return nil, err
	}
	defer temp.Close()
	os.Remove(temp.Name())
	if err := c.Artifact(ctx, op, payload.ArtifactID, temp); err != nil {
		return nil, err
	}
	if _, err := temp.Seek(0, 0); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(temp)
	if err != nil {
		return nil, err
	}
	archive := tar.NewReader(gz)
	var expanded int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gz.Close()
			return nil, err
		}
		if !safeSnapshotEntry(header.Name) || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir) {
			gz.Close()
			return nil, errors.New("unsafe snapshot archive entry")
		}
		expanded += header.Size
		if expanded > 8*1024*1024*1024 {
			gz.Close()
			return nil, errors.New("expanded snapshot too large")
		}
	}
	gz.Close()
	if _, err := temp.Seek(0, 0); err != nil {
		return nil, err
	}
	_, err = r.workspaceExec(ctx, 2*time.Hour, temp, op.Workspace, snapshotExtractArgs("/workspace")...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "restored", "bytes": expanded}, nil
}

// Snapshot archives are created from /workspace with tar -C /workspace .
// The leading component is removed at extraction so an unowned PVC mount
// does not receive metadata operations on its root.
func safeSnapshotEntry(entry string) bool {
	return safeArchiveEntry(entry) &&
		(entry == "." || entry == "./" || strings.HasPrefix(entry, "./") && len(entry) > 2)
}

func snapshotExtractArgs(destination string) []string {
	return []string{"tar", "-xzf", "-", "--no-same-owner", "--no-same-permissions",
		"--no-overwrite-dir", "--touch", "--strip-components=1", "-C", destination}
}

func safeArchiveEntry(entry string) bool {
	if strings.HasPrefix(entry, "/") || strings.ContainsRune(entry, '\x00') {
		return false
	}
	clean := path.Clean(entry)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	for _, part := range strings.Split(entry, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// An empty build context consumes stdin as the Dockerfile. The Docker CLI
// rejects -f - when the context is also -, because both would read stdin.
func dockerBuildArgs(network, image, contextDir string) []string {
	args := []string{"build", "--network", network, "--tag", image}
	if contextDir == "-" {
		return append(args, "-")
	}
	return append(args, "-f", "-", contextDir)
}

func (r *Runner) build(ctx context.Context, p map[string]any) (map[string]any, error) {
	image := getString(p, "image")
	base := getString(p, "base_image")
	if !imageName.MatchString(image) || !r.Images[base] {
		return nil, errors.New("unapproved image build")
	}
	dockerfile := getString(p, "dockerfile")
	contextName := getString(p, "context")
	arch := getString(p, "architecture")
	if arch != "" && arch != runtime.GOARCH {
		return nil, errors.New("environment requires a different worker architecture")
	}
	contextDir := "-"
	if contextName != "" {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`).MatchString(contextName) {
			return nil, errors.New("invalid approved build context name")
		}
		root := os.Getenv("PWM_BUILD_CONTEXT_ROOT")
		if root == "" {
			return nil, errors.New("local build contexts are not enabled")
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		realContext, err := filepath.EvalSymlinks(filepath.Join(realRoot, contextName))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(realRoot, realContext)
		if err != nil || rel != contextName {
			return nil, errors.New("build context escapes allowed directory")
		}
		info, err := os.Stat(realContext)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("build context is not a directory")
		}
		contextDir = realContext
	}
	if len(dockerfile) > 16384 {
		return nil, errors.New("Dockerfile too large")
	}
	if dockerfile != "" {
		directives := []string{}
		bases := []string{}
		for _, line := range strings.Split(dockerfile, "\n") {
			trim := strings.TrimSpace(line)
			if trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			directives = append(directives, trim)
			fields := strings.Fields(trim)
			if len(fields) > 0 && strings.EqualFold(fields[0], "FROM") {
				bases = append(bases, trim)
			}
		}
		if len(directives) == 0 || len(bases) != 1 || !strings.EqualFold(strings.Fields(directives[0])[0], "FROM") {
			return nil, errors.New("Dockerfile must contain one FROM")
		}
		fields := strings.Fields(bases[0])
		if len(fields) != 2 || fields[1] != base {
			return nil, errors.New("unapproved Dockerfile base")
		}
		network := "none"
		if r.BuildNetwork {
			network = "default"
		}
		_, err := r.checked(ctx, 2*time.Hour, strings.NewReader(dockerfile), dockerBuildArgs(network, image, contextDir)...)
		if err != nil {
			return nil, err
		}
	} else {
		if _, err := r.checked(ctx, 5*time.Minute, nil, "tag", base, image); err != nil {
			return nil, err
		}
	}
	result, err := r.checked(ctx, 30*time.Second, nil, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return nil, err
	}
	return map[string]any{"image": image, "image_id": strings.TrimSpace(string(result))}, nil
}

func (r *Runner) Handle(ctx context.Context, op Operation, c *Client) (map[string]any, error) {
	if r.Kube != nil {
		return r.Kube.Handle(ctx, r, op, c)
	}
	payload, err := stringPayload(op.Payload)
	if err != nil {
		return nil, err
	}
	n, err := name(op.Workspace)
	if err != nil {
		return nil, err
	}
	switch op.Action {
	case "start":
		return r.Start(ctx, op.Workspace, payload)
	case "prune_volume":
		return r.PruneVolume(ctx, op.Workspace)
	case "stop":
		_, stderr, status, err := r.docker(ctx, 30*time.Second, nil, "rm", "-f", n)
		if err != nil {
			return nil, err
		}
		if status != 0 && !strings.Contains(string(stderr), "No such container") {
			return nil, fmt.Errorf("docker cleanup failed: %s", stderr)
		}
		return map[string]any{"status": "stopped", "volume": n}, nil
	case "command":
		return r.Command(ctx, op.Workspace, getString(payload, "command"), nil)
	case "read_file":
		p, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		out, err := r.checked(ctx, 120*time.Second, nil, "exec", n, "cat", "--", p)
		if err != nil {
			return nil, err
		}
		if len(out) > 128*1024 {
			return nil, errors.New("file exceeds read limit")
		}
		return map[string]any{"path": getString(payload, "path"), "content": string(out)}, nil
	case "write_file":
		p, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		body := getString(payload, "content")
		if len(body) > 128*1024 {
			return nil, errors.New("file write exceeds limit")
		}
		if expected, ok := payload["expected_sha256"].(string); ok {
			old, err := r.checked(ctx, 30*time.Second, nil, "exec", n, "sha256sum", "--", p)
			current := ""
			if err == nil {
				parts := strings.Fields(string(old))
				if len(parts) > 0 {
					current = parts[0]
				}
			}
			if expected != current {
				return nil, errors.New("file revision conflict")
			}
		}
		cmd := "mkdir -p -- " + shellQuote(path.Dir(p)) + " && cat > " + shellQuote(p)
		result, err := r.Command(ctx, op.Workspace, cmd, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		if result["exit_code"] != 0 {
			return nil, fmt.Errorf("write failed: %v", result["stderr"])
		}
		return map[string]any{"path": getString(payload, "path"), "sha256": HexSHA([]byte(body))}, nil
	case "list_directory":
		item := getString(payload, "path")
		if item == "" {
			item = "repos"
		}
		p, err := safePath(item)
		if err != nil {
			return nil, err
		}
		return r.Command(ctx, op.Workspace, "ls -la -- "+shellQuote(p), nil)
	case "search":
		q := getString(payload, "query")
		if q == "" || len(q) > 256 {
			return nil, errors.New("invalid search term")
		}
		output, err := r.Command(ctx, op.Workspace, "grep -R -n -F -- "+shellQuote(q)+" /workspace 2>/dev/null", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"matches": output["stdout"], "truncated": false}, nil
	case "export_file":
		p, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		return r.saveOutput(ctx, op, c, path.Base(p), nil, "exec", n, "cat", "--", p)
	case "snapshot":
		return r.saveOutput(ctx, op, c, "workspace.tar.gz", nil, "exec", n, "tar", "-czf", "-", "-C", "/workspace", ".")
	case "capture_display":
		return r.captureDisplay(ctx, op, c)
	case "restore_snapshot":
		return r.restore(ctx, op, c)
	case "build_image":
		return r.build(ctx, payload)
	case "export_image":
		image := getString(payload, "image")
		if !imageName.MatchString(image) {
			return nil, errors.New("not an approved environment image")
		}
		return r.saveOutput(ctx, op, c, "environment.tar", nil, "image", "save", image)
	case "load_image":
		image := getString(payload, "image")
		artifactID := getString(payload, "artifact_id")
		if !imageName.MatchString(image) || !workspaceID.MatchString(artifactID) {
			return nil, errors.New("invalid imported environment image")
		}
		file, err := os.CreateTemp("", "pwm-image-import-*")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		os.Remove(file.Name())
		if err := c.Artifact(ctx, op, artifactID, file); err != nil {
			return nil, err
		}
		if _, err := file.Seek(0, 0); err != nil {
			return nil, err
		}
		if _, err := r.checked(ctx, 2*time.Hour, file, "image", "load"); err != nil {
			return nil, err
		}
		id, err := r.checked(ctx, 30*time.Second, nil, "image", "inspect", "--format", "{{.Id}}", image)
		if err != nil {
			return nil, err
		}
		return map[string]any{"image": image, "image_id": strings.TrimSpace(string(id))}, nil
	case "import_file":
		destination, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		artifactID := getString(payload, "artifact_id")
		if !workspaceID.MatchString(artifactID) {
			return nil, errors.New("invalid file artifact ID")
		}
		if c == nil {
			return nil, errors.New("artifact transfer requires a controller")
		}
		file, err := os.CreateTemp("", "pwm-file-import-*")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		os.Remove(file.Name())
		if err := c.Artifact(ctx, op, artifactID, file); err != nil {
			return nil, err
		}
		stat, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if _, err := file.Seek(0, 0); err != nil {
			return nil, err
		}
		// The destination is relative to the workspace and must not exist.
		// Imports cannot silently overwrite a previous file or symlink.
		parent := path.Dir(destination)
		command := "set -eu; umask 077; mkdir -p " + shellQuote(parent) +
			"; [ ! -e " + shellQuote(destination) + " ] && [ ! -L " + shellQuote(destination) + " ]; cat > " + shellQuote(destination)
		if _, err := r.checked(ctx, 2*time.Hour, file, "exec", "-i", n, "sh", "-c", command); err != nil {
			return nil, err
		}
		return map[string]any{"path": getString(payload, "path"), "size": stat.Size()}, nil
	case "import_archive":
		return r.importArchive(ctx, op, c)
	default:
		return nil, fmt.Errorf("unsupported workspace operation %q", op.Action)
	}
}

func (r *Runner) importArchive(ctx context.Context, op Operation, c *Client) (map[string]any, error) {
	var p struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(op.Payload, &p); err != nil {
		return nil, err
	}
	repoName := regexp.MustCompile(`^(local:)?[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	if !repoName.MatchString(p.Repository) || !workspaceID.MatchString(p.ArtifactID) {
		return nil, errors.New("invalid imported repository")
	}
	temp, err := os.CreateTemp("", "pwm-repository-*")
	if err != nil {
		return nil, err
	}
	defer temp.Close()
	os.Remove(temp.Name())
	if err := c.Artifact(ctx, op, p.ArtifactID, temp); err != nil {
		return nil, err
	}
	if _, err := temp.Seek(0, 0); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(temp)
	if err != nil {
		return nil, err
	}
	source := tar.NewReader(gz)
	var packed bytes.Buffer
	dest := tar.NewWriter(&packed)
	var total int64
	count := 0
	for {
		header, err := source.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		count++
		if count > 10000 {
			return nil, errors.New("too many repository files")
		}
		components := strings.Split(header.Name, "/")
		if len(components) < 2 {
			continue
		}
		relative := strings.Join(components[1:], "/")
		if !safeArchiveEntry(relative) || relative == "." {
			return nil, errors.New("unsafe repository archive entry")
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, errors.New("repository symlinks and special files are forbidden")
		}
		total += header.Size
		if total > 32*1024*1024 {
			return nil, errors.New("repository snapshot exceeds 32 MiB")
		}
		entry := &tar.Header{Name: "repos/" + strings.NewReplacer("/", "__", ":", "__").Replace(p.Repository) + "/" + relative,
			Mode: 0644, Size: header.Size, Typeflag: tar.TypeReg, Uid: 65534, Gid: 65534}
		if header.Mode&0111 != 0 {
			entry.Mode = 0755
		}
		if err := dest.WriteHeader(entry); err != nil {
			return nil, err
		}
		if _, err := io.CopyN(dest, source, header.Size); err != nil {
			return nil, err
		}
	}
	if err := dest.Close(); err != nil {
		return nil, err
	}
	gz.Close()
	if _, err := r.workspaceExec(ctx, 5*time.Minute, nil, op.Workspace, "mkdir", "-p", "/workspace/repos"); err != nil {
		return nil, err
	}
	if _, err := r.workspaceExec(ctx, 5*time.Minute, bytes.NewReader(packed.Bytes()), op.Workspace,
		"tar", "-xf", "-", "--no-same-owner", "--no-same-permissions", "-C", "/workspace"); err != nil {
		return nil, err
	}
	return map[string]any{"repository": p.Repository, "revision": p.Revision, "files_bytes": total}, nil
}

// cleanupInterruptedJob confirms that a cancelled Docker CLI did not leave a
// short-lived job container running. The exact container name is controller
// derived; cleanup never targets unrelated containers or named volumes.
func (r *Runner) cleanupInterruptedJob(container string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, _ = r.docker(ctx, 20*time.Second, nil, "rm", "-f", container)
	output, stderr, status, err := r.docker(ctx, 10*time.Second, nil, "ps", "-a",
		"--filter", "name=^/"+container+"$", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("cannot verify cancelled job cleanup: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("cannot verify cancelled job cleanup: %s", strings.TrimSpace(string(stderr)))
	}
	if strings.TrimSpace(string(output)) != "" {
		return errors.New("cancelled job container remains after cleanup")
	}
	return nil
}

var workerNodeName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// ReconcileStaleJobs removes only this worker's managed one-shot containers.
// It runs after successful authentication and before the worker claims work.
// Jobs created by older versions without worker labels need manual review.
func (r *Runner) ReconcileStaleJobs(ctx context.Context, node string) error {
	if r.Kube != nil {
		r.Kube.Worker = node
		return r.Kube.ReconcileStaleJobs(ctx)
	}
	if !workerNodeName.MatchString(node) {
		return errors.New("invalid worker ID")
	}
	output, stderr, status, err := r.docker(ctx, 15*time.Second, nil,
		"ps", "-a", "--filter", "label=project-workspace=job",
		"--filter", "label=project-workspace-worker="+node, "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("list stale worker jobs: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("list stale worker jobs: %s", strings.TrimSpace(string(stderr)))
	}
	names := make([]string, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		container := strings.TrimSpace(line)
		if container == "" {
			continue
		}
		if !strings.HasPrefix(container, "pwm-job-") ||
			!workspaceID.MatchString(strings.TrimPrefix(container, "pwm-job-")) {
			return errors.New("unexpected container in worker cleanup inventory")
		}
		if !seen[container] {
			names = append(names, container)
			seen[container] = true
		}
		if len(names) > 128 {
			return errors.New("too many managed worker jobs to reconcile")
		}
	}
	for _, container := range names {
		if err := r.cleanupInterruptedJob(container); err != nil {
			return fmt.Errorf("reconcile %s: %w", container, err)
		}
	}
	return nil
}

func (r *Runner) ExecuteJob(ctx context.Context, job Job) (string, string) {
	if r.Kube != nil {
		return r.Kube.ExecuteJob(ctx, r, job)
	}
	if !workspaceID.MatchString(job.ID) {
		return "failed", "invalid job ID"
	}
	if !r.Images[job.Image] {
		return "failed", "unapproved container image"
	}
	container := "pwm-job-" + job.ID
	if r.WorkerID != "" && !workerNodeName.MatchString(r.WorkerID) {
		return "failed", "invalid worker ID"
	}
	args := []string{"run", "--rm", "--name", container, "--label", "project-workspace=job",
		"--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", "128",
		"--cpus", strconv.FormatFloat(job.CPU, 'f', 2, 64),
		"--memory", fmt.Sprintf("%dm", job.MemoryMB),
		"--memory-swap", fmt.Sprintf("%dm", job.MemoryMB),
		"--tmpfs", "/tmp:rw,nosuid,noexec,size=64m", "--user", "65534:65534"}
	if r.WorkerID != "" {
		args = append(args, "--label", "project-workspace-worker="+r.WorkerID)
	}
	devices, err := DeviceDockerArgsAuthorized(job.Devices, r.AvailableDevices(), false)
	if err != nil {
		return "failed", fmt.Sprintf("unapproved job device lease: %v", err)
	}
	args = append(args, devices...)
	args = append(args, job.Image, "sh", "-c", job.Command)
	out, stderr, status, err := r.docker(ctx, 5*time.Minute, nil, args...)
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		reason := err
		if ctx.Err() != nil {
			reason = ctx.Err()
		}
		if cleanupErr := r.cleanupInterruptedJob(container); cleanupErr != nil {
			return "failed", fmt.Sprintf("%v; %v", reason, cleanupErr)
		}
		return "failed", reason.Error()
	}
	if err != nil {
		// A failed Docker client can leave a job running in the daemon.
		// Verify that its exact managed container has been removed.
		if cleanupErr := r.cleanupInterruptedJob(container); cleanupErr != nil {
			return "failed", fmt.Sprintf("%v; %v", err, cleanupErr)
		}
		return "failed", err.Error()
	}
	if status != 0 {
		// A failed command normally exits with --rm cleanup, but a Docker
		// client or daemon failure can leave the named container behind.
		if cleanupErr := r.cleanupInterruptedJob(container); cleanupErr != nil {
			return "failed", fmt.Sprintf("docker exit %d; %v", status, cleanupErr)
		}
	}
	result := string(out) + "\n" + string(stderr)
	if len(result) > 32768 {
		result = result[len(result)-32768:]
	}
	if status != 0 {
		if strings.TrimSpace(result) == "" {
			return "failed", fmt.Sprintf("docker exit %d", status)
		}
		return "failed", result
	}
	return "succeeded", result
}

// Ensure archive names and transfer checks stay in one place.
var _ = base64.StdEncoding
