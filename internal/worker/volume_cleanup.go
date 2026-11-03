package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PruneVolume removes only a stopped workspace's labeled Docker volume.
// The controller must first verify its durable checkpoint and finished stop.
func (r *Runner) PruneVolume(ctx context.Context, workspace string) (map[string]any, error) {
	volume, err := name(workspace)
	if err != nil {
		return nil, err
	}
	data, err := r.checked(ctx, 15*time.Second, nil, "volume", "inspect",
		"--format", "{{json .Labels}}", volume)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect retained volume: %w", err)
	}
	var labels map[string]string
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil, errors.New("invalid Docker volume labels")
	}
	if labels["project-workspace"] != "volume" ||
		labels["project-workspace-workspace"] != workspace {
		return nil, errors.New("volume is not labeled for the requested workspace")
	}
	attached, err := r.checked(ctx, 15*time.Second, nil, "ps", "-a",
		"--filter", "volume="+volume, "--format", "{{.ID}}")
	if err != nil {
		return nil, fmt.Errorf("cannot inspect volume attachments: %w", err)
	}
	if strings.TrimSpace(string(attached)) != "" {
		return nil, errors.New("volume is still attached to a container")
	}
	if _, err := r.checked(ctx, 30*time.Second, nil, "volume", "rm", volume); err != nil {
		return nil, fmt.Errorf("Docker volume cleanup failed: %w", err)
	}
	_, stderr, status, err := r.docker(ctx, 15*time.Second, nil, "volume", "inspect", volume)
	if err != nil {
		return nil, fmt.Errorf("cannot verify volume cleanup: %w", err)
	}
	if status == 0 || !strings.Contains(strings.ToLower(string(stderr)), "no such volume") {
		return nil, errors.New("cannot confirm that the volume was removed")
	}
	return map[string]any{"status": "pruned", "volume": volume}, nil
}
