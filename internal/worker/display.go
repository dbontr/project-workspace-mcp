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
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxDisplayPNGBytes = 8 << 20

var displayDimensions = regexp.MustCompile(`(?m)^\s*dimensions:\s+([0-9]{1,5})x([0-9]{1,5})\s+pixels`)

func validDisplaySize(width, height int) bool {
	return width >= 320 && width <= 3840 && height >= 240 &&
		height <= 2160 && width*height <= 8_000_000
}

// startVirtualDisplay creates an X server inside the isolated workspace.
// It never mounts or connects to the host's physical display.
func (r *Runner) startVirtualDisplay(ctx context.Context, container string, width, height int) error {
	if !validDisplaySize(width, height) {
		return errors.New("invalid virtual display size")
	}
	if _, err := r.checked(ctx, 10*time.Second, nil, "exec", container, "sh", "-c",
		"command -v Xvfb >/dev/null && command -v xdpyinfo >/dev/null && command -v ffmpeg >/dev/null"); err != nil {
		return errors.New("display image requires Xvfb, xdpyinfo and ffmpeg")
	}
	size := fmt.Sprintf("%dx%dx24", width, height)
	if _, err := r.checked(ctx, 10*time.Second, nil, "exec", "-d", container,
		"Xvfb", ":99", "-screen", "0", size, "-nolisten", "tcp", "-noreset"); err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		if _, err := r.checked(ctx, 3*time.Second, nil, "exec", container, "xdpyinfo", "-display", ":99"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("virtual display did not become ready")
}

func (r *Runner) captureDisplay(ctx context.Context, op Operation, client *Client) (map[string]any, error) {
	if client == nil {
		return nil, errors.New("display capture requires a controller")
	}
	container, err := name(op.Workspace)
	if err != nil {
		return nil, err
	}
	info, err := r.checked(ctx, 10*time.Second, nil, "exec", container, "xdpyinfo", "-display", ":99")
	if err != nil {
		return nil, fmt.Errorf("workspace virtual display is unavailable: %w", err)
	}
	dim := displayDimensions.FindStringSubmatch(string(info))
	if len(dim) != 3 {
		return nil, errors.New("cannot determine virtual display size")
	}
	width, err := strconv.Atoi(dim[1])
	if err != nil {
		return nil, err
	}
	height, err := strconv.Atoi(dim[2])
	if err != nil || !validDisplaySize(width, height) {
		return nil, errors.New("invalid virtual display dimensions")
	}

	file, err := os.CreateTemp("", "pwm-display-*.png")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	_ = os.Remove(file.Name())

	captureCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(captureCtx, "docker", "exec", container,
		"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "x11grab", "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", "1", "-i", ":99", "-frames:v", "1",
		"-f", "image2pipe", "-vcodec", "png", "-")
	var stderr bytes.Buffer
	command.Stderr = &limitWriter{Writer: &stderr, Limit: 4096}
	digest := sha256.New()
	sizeLimit := int64(maxDisplayPNGBytes)
	if r.MaxArtifact < sizeLimit {
		sizeLimit = r.MaxArtifact
	}
	command.Stdout = &maxWriter{Writer: io.MultiWriter(file, digest), Limit: sizeLimit}
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("display capture failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(file)
	if err != nil || config.Width != width || config.Height != height {
		return nil, errors.New("captured image has invalid PNG dimensions")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err := png.Decode(file); err != nil {
		return nil, fmt.Errorf("captured PNG is corrupt: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	artifactID, err := client.Upload(ctx, op, "display.png", file, hex.EncodeToString(digest.Sum(nil)))
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact_id": artifactID, "width": width, "height": height, "mime_type": "image/png"}, nil
}
