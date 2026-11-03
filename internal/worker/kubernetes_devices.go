package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// KubernetesDevice describes one schedulable unit of an operator-approved
// extended resource. It does not represent an arbitrary host device mapping.
// Kubernetes device plugins provide access when the Pod is scheduled.
type KubernetesDevice struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Model    string `json:"model,omitempty"`
	MemoryMB int64  `json:"memory_mb,omitempty"`
	Resource string `json:"resource"`
}

var kubeResourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,251}/[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func LoadKubernetesDevices(filename string) ([]KubernetesDevice, error) {
	if filename == "" {
		return nil, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("Kubernetes device manifest: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32768 {
		return nil, errors.New("Kubernetes device manifest must be a bounded regular file")
	}
	var input struct {
		Devices []KubernetesDevice `json:"devices"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("invalid Kubernetes device manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("Kubernetes device manifest must contain exactly one JSON object")
	}
	if len(input.Devices) > 64 {
		return nil, errors.New("too many Kubernetes device slots")
	}
	ids := map[string]bool{}
	kindByResource := map[string]string{}
	for _, d := range input.Devices {
		if !deviceID.MatchString(d.ID) || ids[d.ID] ||
			(d.Kind != "gpu" && d.Kind != "npu" && d.Kind != "fpga") {
			return nil, errors.New("invalid Kubernetes device ID or kind")
		}
		if len(d.Model) > 128 || d.MemoryMB < 0 || d.MemoryMB > 1000000 ||
			!kubeResourceName.MatchString(d.Resource) {
			return nil, errors.New("invalid Kubernetes device resource or metadata")
		}
		domain := strings.SplitN(d.Resource, "/", 2)[0]
		if domain == "kubernetes.io" || strings.HasSuffix(domain, ".kubernetes.io") ||
			domain == "k8s.io" || strings.HasSuffix(domain, ".k8s.io") ||
			strings.Contains(domain, "..") || strings.HasSuffix(domain, ".") ||
			strings.HasPrefix(domain, ".") {
			return nil, errors.New("Kubernetes device resource must use an operator-approved vendor domain")
		}
		if prior := kindByResource[d.Resource]; prior != "" && prior != d.Kind {
			return nil, errors.New("Kubernetes device resource cannot describe different hardware kinds")
		}
		ids[d.ID] = true
		kindByResource[d.Resource] = d.Kind
	}
	return input.Devices, nil
}

func PublicKubernetesDevices(devices []KubernetesDevice) []any {
	result := make([]any, 0, len(devices))
	for _, d := range devices {
		item := map[string]any{"id": d.ID, "kind": d.Kind}
		if d.Model != "" {
			item["model"] = d.Model
		}
		if d.MemoryMB > 0 {
			item["memory_mb"] = d.MemoryMB
		}
		result = append(result, item)
	}
	return result
}
