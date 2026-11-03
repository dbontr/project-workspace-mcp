package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type Device struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Model    string   `json:"model,omitempty"`
	MemoryMB int64    `json:"memory_mb,omitempty"`
	CDI      string   `json:"cdi,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

type DeviceClaim struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

var deviceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var cdiName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+=[A-Za-z0-9_.:-]+$`)

func LoadDevices(path string) ([]Device, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Devices []Device `json:"devices"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("device manifest: %w", err)
	}
	if len(manifest.Devices) > 64 {
		return nil, errors.New("too many devices")
	}
	ids := make(map[string]bool)
	for _, d := range manifest.Devices {
		if !deviceID.MatchString(d.ID) || (d.Kind != "gpu" && d.Kind != "npu" && d.Kind != "fpga" && d.Kind != "usb") || ids[d.ID] {
			return nil, errors.New("invalid or duplicate device ID")
		}
		ids[d.ID] = true
		if len(d.Model) > 128 || d.MemoryMB < 0 || d.MemoryMB > 10_000_000 {
			return nil, errors.New("invalid device metadata")
		}
		if d.Kind == "usb" && (d.CDI != "" || len(d.Paths) != 1 || !usbPathAllowed(d.Paths[0])) {
			return nil, errors.New("USB devices require a single stable USB path, not CDI")
		}
		if d.CDI != "" && !cdiName.MatchString(d.CDI) {
			return nil, errors.New("invalid CDI device")
		}
		if len(d.Paths) > 8 || (d.CDI == "" && len(d.Paths) == 0) {
			return nil, errors.New("missing or excessive device mappings")
		}
		for _, p := range d.Paths {
			if len(p) > 255 || !strings.HasPrefix(p, "/dev/") || strings.Contains(p, "..") {
				return nil, errors.New("device policy requires explicit /dev paths")
			}
		}
	}
	return manifest.Devices, nil
}

func DeviceDockerArgs(claims []DeviceClaim, known []Device) ([]string, error) {
	return DeviceDockerArgsAuthorized(claims, known, false)
}

// DeviceDockerArgsAuthorized only maps explicitly leased, operator-approved hardware.
func DeviceDockerArgsAuthorized(claims []DeviceClaim, known []Device, allowUSB bool) ([]string, error) {
	if len(claims) > 8 {
		return nil, errors.New("too many hardware allocations")
	}
	indexed := map[string]Device{}
	for _, d := range known {
		indexed[d.ID] = d
	}
	used := map[string]bool{}
	args := []string{}
	for _, claim := range claims {
		d, ok := indexed[claim.ID]
		if !ok || used[claim.ID] || d.Kind != claim.Kind {
			return nil, errors.New("hardware lease does not match local device policy")
		}
		used[claim.ID] = true
		if d.Kind == "usb" {
			if !allowUSB {
				return nil, errors.New("USB passthrough is disabled")
			}
			mapped, group, err := usbDockerMapping(d.Paths[0])
			if err != nil {
				return nil, err
			}
			args = append(args, "--device", mapped+":"+mapped+":rwm", "--group-add", fmt.Sprint(group))
			continue
		}
		if d.CDI != "" {
			args = append(args, "--device", d.CDI)
		}
		for _, p := range d.Paths {
			clean := filepath.Clean(p)
			if clean != p || !strings.HasPrefix(clean, "/dev/") {
				return nil, errors.New("invalid device path")
			}
			var info syscall.Stat_t
			// lstat detects symlink aliases; only character devices may be mapped.
			if err := syscall.Lstat(clean, &info); err != nil {
				return nil, fmt.Errorf("device %s: %w", clean, err)
			}
			if info.Mode&syscall.S_IFMT != syscall.S_IFCHR {
				return nil, errors.New("configured path is not a character device")
			}
			args = append(args, "--device", clean)
		}
	}
	return args, nil
}

func PublicDevices(devices []Device) []map[string]any {
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		entry := map[string]any{"id": d.ID, "kind": d.Kind}
		if d.Model != "" {
			entry["model"] = d.Model
		}
		if d.MemoryMB > 0 {
			entry["memory_mb"] = d.MemoryMB
		}
		out = append(out, entry)
	}
	return out
}
