package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

func (k *Kubernetes) Handle(ctx context.Context, r *Runner, op Operation, c *Client) (map[string]any, error) {
	payload, err := stringPayload(op.Payload)
	if err != nil {
		return nil, err
	}
	if !workspaceID.MatchString(op.Workspace) {
		return nil, errors.New("invalid workspace ID")
	}
	switch op.Action {
	case "start":
		return k.Start(ctx, r, op.Workspace, payload)
	case "stop":
		return k.Stop(ctx, op.Workspace)
	case "prune_volume":
		return k.PruneVolume(ctx, op.Workspace)
	case "command":
		return k.Command(ctx, op.Workspace, getString(payload, "command"), nil)
	case "read_file":
		filename, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		out, err := r.workspaceExec(ctx, 120*time.Second, nil, op.Workspace, "cat", "--", filename)
		if err != nil {
			return nil, err
		}
		if len(out) > 128*1024 {
			return nil, errors.New("file exceeds read limit")
		}
		return map[string]any{"path": getString(payload, "path"), "content": string(out)}, nil
	case "write_file":
		return k.writeFile(ctx, r, op.Workspace, payload)
	case "list_directory":
		directory := getString(payload, "path")
		if directory == "" {
			directory = "repos"
		}
		filename, err := safePath(directory)
		if err != nil {
			return nil, err
		}
		return k.Command(ctx, op.Workspace, "ls -la -- "+shellQuote(filename), nil)
	case "search":
		query := getString(payload, "query")
		if query == "" || len(query) > 256 {
			return nil, errors.New("invalid search query")
		}
		output, err := k.Command(ctx, op.Workspace,
			"grep -R -n -F -- "+shellQuote(query)+" /workspace 2>/dev/null", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"matches": output["stdout"], "truncated": false}, nil
	case "export_file":
		filename, err := safePath(getString(payload, "path"))
		if err != nil {
			return nil, err
		}
		return k.saveOutput(ctx, r, op, c, path.Base(filename), "cat", "--", filename)
	case "snapshot":
		return k.saveOutput(ctx, r, op, c, "workspace.tar.gz",
			"tar", "-czf", "-", "-C", "/workspace", ".")
	case "restore_snapshot":
		return r.restore(ctx, op, c)
	case "import_file":
		return k.importFile(ctx, r, op, c, payload)
	case "import_archive":
		return r.importArchive(ctx, op, c)
	case "capture_display":
		return k.captureDisplay(ctx, r, op, c)
	case "build_image", "export_image", "load_image":
		return nil, errors.New("image build and transfer require an approved Kubernetes registry builder")
	default:
		return nil, fmt.Errorf("unsupported Kubernetes operation %q", op.Action)
	}
}

func (k *Kubernetes) writeFile(ctx context.Context, r *Runner, id string, p map[string]any) (map[string]any, error) {
	filename, err := safePath(getString(p, "path"))
	if err != nil {
		return nil, err
	}
	body := getString(p, "content")
	if len(body) > 128*1024 {
		return nil, errors.New("file write exceeds limit")
	}
	if expected, ok := p["expected_sha256"].(string); ok {
		out, err := r.workspaceExec(ctx, 30*time.Second, nil, id,
			"sha256sum", "--", filename)
		actual := ""
		if err == nil {
			fields := strings.Fields(string(out))
			if len(fields) > 0 {
				actual = fields[0]
			}
		}
		if expected != actual {
			return nil, errors.New("file revision conflict")
		}
	}
	command := "umask 077; mkdir -p -- " + shellQuote(path.Dir(filename)) +
		" && cat > " + shellQuote(filename)
	if _, err := r.workspaceExec(ctx, 2*time.Minute, strings.NewReader(body), id,
		"sh", "-c", command); err != nil {
		return nil, err
	}
	return map[string]any{"path": getString(p, "path"),
		"sha256": HexSHA([]byte(body))}, nil
}

// saveOutput streams a Pod's output to the controller's immutable artifact
// store; no path inside the transient worker Pod becomes canonical storage.
func (k *Kubernetes) saveOutput(ctx context.Context, r *Runner, op Operation, c *Client, filename string, command ...string) (map[string]any, error) {
	if c == nil {
		return nil, errors.New("artifact output requires a controller")
	}
	if err := k.requireWorkspace(ctx, op.Workspace); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "pwm-kube-output-*")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_ = os.Remove(f.Name())
	digest := sha256.New()
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	args := append(k.baseArgs(), "exec", "pod/"+kubePodName(op.Workspace), "-c", "workspace", "--")
	args = append(args, command...)
	cmd := exec.CommandContext(runCtx, k.Binary, args...)
	cmd.Stdout = &maxWriter{Writer: io.MultiWriter(f, digest), Limit: r.MaxArtifact}
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{Writer: &stderr, Limit: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Kubernetes artifact export failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	id, err := c.Upload(ctx, op, filename, f, hex.EncodeToString(digest.Sum(nil)))
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact_id": id}, nil
}

func (k *Kubernetes) importFile(ctx context.Context, r *Runner, op Operation, c *Client, payload map[string]any) (map[string]any, error) {
	destination, err := safePath(getString(payload, "path"))
	if err != nil {
		return nil, err
	}
	artifactID := getString(payload, "artifact_id")
	if !workspaceID.MatchString(artifactID) || c == nil {
		return nil, errors.New("invalid artifact import request")
	}
	file, err := os.CreateTemp("", "pwm-kube-import-*")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	_ = os.Remove(file.Name())
	if err := c.Artifact(ctx, op, artifactID, file); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	command := "set -eu; umask 077; mkdir -p -- " + shellQuote(path.Dir(destination)) +
		"; [ ! -e " + shellQuote(destination) + " ] && [ ! -L " + shellQuote(destination) + " ]; cat > " + shellQuote(destination)
	if _, err := r.workspaceExec(ctx, 2*time.Hour, file, op.Workspace, "sh", "-c", command); err != nil {
		return nil, err
	}
	return map[string]any{"path": getString(payload, "path"), "size": info.Size()}, nil
}

func (k *Kubernetes) captureDisplay(ctx context.Context, r *Runner, op Operation, c *Client) (map[string]any, error) {
	if c == nil || !workspaceID.MatchString(op.Workspace) {
		return nil, errors.New("invalid display capture request")
	}
	if err := k.requireWorkspace(ctx, op.Workspace); err != nil {
		return nil, err
	}
	info, err := r.workspaceExec(ctx, 15*time.Second, nil, op.Workspace, "xdpyinfo", "-display", ":99")
	if err != nil {
		return nil, err
	}
	dim := displayDimensions.FindStringSubmatch(string(info))
	if len(dim) != 3 {
		return nil, errors.New("workspace display dimensions unavailable")
	}
	width, err := strconv.Atoi(dim[1])
	if err != nil {
		return nil, err
	}
	height, err := strconv.Atoi(dim[2])
	if err != nil || !validDisplaySize(width, height) {
		return nil, errors.New("invalid display capture dimensions")
	}
	file, err := os.CreateTemp("", "pwm-kube-display-*.png")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	_ = os.Remove(file.Name())
	digest := sha256.New()
	args := append(k.baseArgs(), "exec", "pod/"+kubePodName(op.Workspace),
		"-c", "workspace", "--", "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-nostdin", "-f", "x11grab", "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", "1", "-i", ":99", "-frames:v", "1", "-f", "image2pipe",
		"-vcodec", "png", "-")
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, k.Binary, args...)
	limit := int64(maxDisplayPNGBytes)
	if r.MaxArtifact < limit {
		limit = r.MaxArtifact
	}
	cmd.Stdout = &maxWriter{Writer: io.MultiWriter(file, digest), Limit: limit}
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{Writer: &stderr, Limit: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("workspace display capture failed: %w: %s", err, stderr.String())
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(file)
	if err != nil || config.Width != width || config.Height != height {
		return nil, errors.New("captured image dimensions do not match the workspace display")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err := png.Decode(file); err != nil {
		return nil, errors.New("captured PNG is invalid")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	id, err := c.Upload(ctx, op, "display.png", file, hex.EncodeToString(digest.Sum(nil)))
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact_id": id, "width": width, "height": height,
		"mime_type": "image/png"}, nil
}
