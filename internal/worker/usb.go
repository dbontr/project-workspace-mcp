package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// USB disk access is disabled unless the worker operator and workspace request
// both enable it. USB block devices must have stable USB by-id names. Never
// expose a mounted disk (or any of its mounted partitions) to an OS flasher.
var busUSB = regexp.MustCompile(`^/dev/bus/usb/[0-9]{3}/[0-9]{3}$`)
var usbDiskByID = regexp.MustCompile(`^/dev/disk/by-id/usb-[A-Za-z0-9_.:+-]+$`)
var usbSerialByID = regexp.MustCompile(`^/dev/serial/by-id/[A-Za-z0-9_.:+-]+$`)
var usbBusSegment = regexp.MustCompile(`^usb[0-9]+$`)

func usbPathAllowed(p string) bool {
	return usbDiskByID.MatchString(p) && !strings.Contains(p, "-part") ||
		usbSerialByID.MatchString(p) || busUSB.MatchString(p)
}

func usbSysfsPath(devicePath string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("cannot identify USB device")
	}
	raw := uint64(stat.Rdev)
	major := ((raw >> 8) & 0xfff) | ((raw >> 32) & ^uint64(0xfff))
	minor := (raw & 0xff) | ((raw >> 12) & 0xffffff00)
	kind := "char"
	if info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0 {
		kind = "block"
	}
	resolved, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/%s/%d:%d", kind, major, minor))
	if err != nil {
		return "", fmt.Errorf("USB sysfs identity unavailable: %w", err)
	}
	for _, part := range strings.Split(filepath.Clean(resolved), string(filepath.Separator)) {
		if usbBusSegment.MatchString(part) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("device %s does not belong to a USB bus", devicePath)
}

// usbDockerMapping validates live identity at the moment a lease starts.
// The returned group is the mapped device's host group, not a blanket root user.
func usbDockerMapping(configured string) (string, uint32, error) {
	if !usbPathAllowed(configured) {
		return "", 0, errors.New("USB path must be a stable by-id alias or bus device")
	}
	resolved, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", 0, err
	}
	if !strings.HasPrefix(resolved, "/dev/") {
		return "", 0, errors.New("USB path escaped /dev")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeDevice == 0 {
		return "", 0, errors.New("USB mapping is not a device")
	}
	block := info.Mode()&os.ModeCharDevice == 0
	if usbDiskByID.MatchString(configured) != block {
		return "", 0, errors.New("USB device type does not match its approved path")
	}
	if _, err := usbSysfsPath(resolved, info); err != nil {
		return "", 0, err
	}
	if block {
		// Fail closed when lsblk is unavailable or cannot inspect the entire disk.
		typ, err := exec.Command("lsblk", "-dnro", "TYPE", resolved).Output()
		if err != nil || strings.TrimSpace(string(typ)) != "disk" {
			return "", 0, errors.New("USB storage must be a whole disk")
		}
		kinds, err := exec.Command("lsblk", "-nro", "TYPE", resolved).Output()
		if err != nil {
			return "", 0, errors.New("USB disk usage could not be inspected")
		}
		for _, kind := range strings.Fields(string(kinds)) {
			if kind != "disk" && kind != "part" {
				return "", 0, errors.New("USB disk backs an active mapped device")
			}
		}
		mounts, err := exec.Command("lsblk", "-nro", "MOUNTPOINTS", resolved).Output()
		if err != nil || strings.TrimSpace(string(mounts)) != "" {
			return "", 0, errors.New("USB disk or one of its partitions is mounted or in use")
		}
	}
	stat := info.Sys().(*syscall.Stat_t)
	return resolved, stat.Gid, nil
}

// DiscoverUSBDisks is deliberately limited to stable by-id USB whole disks;
// it does not automatically enroll serial adapters, JTAG probes, or host disks.
func DiscoverUSBDisks() []Device {
	paths, err := filepath.Glob("/dev/disk/by-id/usb-*")
	if err != nil {
		return nil
	}
	devices := make([]Device, 0, len(paths))
	for _, p := range paths {
		if len(devices) >= 64 {
			break
		}
		if !usbDiskByID.MatchString(p) {
			continue
		}
		if _, _, err := usbDockerMapping(p); err != nil {
			continue
		}
		digest := sha256.Sum256([]byte(p))
		model := filepath.Base(p)
		if len(model) > 128 {
			model = model[:128]
		}
		devices = append(devices, Device{
			ID: "usb-" + hex.EncodeToString(digest[:8]), Kind: "usb", Model: model, Paths: []string{p},
		})
	}
	return devices
}
