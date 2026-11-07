// Package worker implements a Linux Docker node agent using the controller's
// authenticated, claim-fenced API. It does not need a local Python interpreter.
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
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Base  string
	Token string
	Node  string
	HTTP  *http.Client
}

type Operation struct {
	ID         string          `json:"id"`
	Workspace  string          `json:"workspace"`
	Action     string          `json:"action"`
	Payload    json.RawMessage `json:"payload"`
	LeaseToken string          `json:"lease_token"`
}

type Job struct {
	ID         string        `json:"id"`
	Command    string        `json:"command"`
	Image      string        `json:"image"`
	CPU        float64       `json:"cpu"`
	MemoryMB   int           `json:"memory_mb"`
	LeaseToken string        `json:"lease_token"`
	Devices    []DeviceClaim `json:"devices,omitempty"`
}

func ValidateURL(address string) (string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Host == "" {
		return "", errors.New("controller URL must contain scheme and host only")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1")) {
		return "", errors.New("remote controller requires HTTPS")
	}
	return strings.TrimRight(address, "/"), nil
}

func (c *Client) request(ctx context.Context, method, path string, input any, output any) error {
	var source io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		source = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, source)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return &HTTPStatusError{Code: res.StatusCode, Method: method, Path: path, Body: string(body)}
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 8*1024*1024)).Decode(output)
	}
	return nil
}

// HTTPStatusError keeps permanent credential and authorization failures distinct
// from network and server failures. Do not retry invalid credentials indefinitely.
type HTTPStatusError struct {
	Code   int
	Method string
	Path   string
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("controller %s %s: HTTP %d: %s", e.Method, e.Path, e.Code, e.Body)
}

func retryableControllerError(err error) bool {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Code == 408 || status.Code == 429 || status.Code >= 500
	}
	return true
}

func retryController(ctx context.Context, err error, attempts *int) error {
	if !retryableControllerError(err) {
		return err
	}
	*attempts++
	power := *attempts - 1
	if power > 5 {
		power = 5
	}
	delay := time.Second * time.Duration(1<<power)
	log.Printf("controller temporarily unavailable (%v); retrying in %s", err, delay)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (c *Client) Pair(ctx context.Context, code string, capabilities map[string]any) (string, error) {
	var response struct {
		WorkerToken string `json:"worker_token"`
	}
	err := c.request(ctx, http.MethodPost, "/api/pair", map[string]any{
		"pairing_code": code, "name": c.Node, "capabilities": capabilities}, &response)
	return response.WorkerToken, err
}

// heartbeat reports current node capacity independently of work execution.
func (c *Client) heartbeat(ctx context.Context, runner *Runner) error {
	capabilities, err := runner.Capabilities()
	if err != nil {
		return err
	}
	return c.request(ctx, "POST", fmt.Sprintf("/api/worker/%s/heartbeat", c.Node),
		map[string]any{"capabilities": capabilities}, nil)
}

// heartbeatWhileRunning keeps a busy worker visible to the scheduler. A
// revoked worker credential ends execution instead of allowing new work.
func (c *Client) heartbeatWhileRunning(ctx context.Context, runner *Runner, interval time.Duration, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			err := c.heartbeat(requestCtx, runner)
			stop()
			if err == nil || ctx.Err() != nil {
				continue
			}
			if !retryableControllerError(err) {
				cancel(fmt.Errorf("worker heartbeat rejected: %w", err))
				return
			}
			log.Printf("worker heartbeat temporarily failed: %v", err)
		}
	}
}

func (c *Client) Run(ctx context.Context, runner *Runner) error {
	return c.runWithHeartbeatInterval(ctx, runner, 30*time.Second)
}

// runWithHeartbeatInterval permits fast lifecycle tests without changing
// the production heartbeat cadence.
func (c *Client) runWithHeartbeatInterval(parent context.Context, runner *Runner, interval time.Duration) error {
	if c.Node == "" || c.Token == "" || c.HTTP == nil || runner == nil {
		return errors.New("worker requires node credential and runner")
	}
	ctx, cancel := context.WithCancelCause(parent)
	runner.WorkerID = c.Node
	reconciled := false
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		c.heartbeatWhileRunning(ctx, runner, interval, cancel)
	}()
	defer func() {
		cancel(nil)
		<-heartbeatDone
	}()
	attempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if err := c.heartbeat(ctx, runner); err != nil {
			if failure := retryController(ctx, err, &attempts); failure != nil {
				return failure
			}
			continue
		}
		if runner.ReconcileOnConnect && !reconciled {
			if err := runner.ReconcileStaleJobs(ctx, c.Node); err != nil {
				return fmt.Errorf("stale job recovery: %w", err)
			}
			// Runtime cleanup must succeed before the controller fences old
			// claims and admits new work on this worker.
			if err := c.request(ctx, "POST",
				fmt.Sprintf("/api/worker/%s/reconcile", c.Node), nil, nil); err != nil {
				return fmt.Errorf("record worker reconciliation: %w", err)
			}
			reconciled = true
		}
		var op struct {
			Operation *Operation `json:"operation"`
		}
		if err := c.request(ctx, "POST", fmt.Sprintf("/api/worker/%s/operation/claim", c.Node), nil, &op); err != nil {
			if failure := retryController(ctx, err, &attempts); failure != nil {
				return failure
			}
			continue
		}
		if op.Operation != nil {
			if err := c.executeOperation(ctx, runner, *op.Operation); err != nil {
				if failure := retryController(ctx, err, &attempts); failure != nil {
					return failure
				}
				continue
			}
			attempts = 0
			continue
		}
		var job struct {
			Job *Job `json:"job"`
		}
		if err := c.request(ctx, "POST", fmt.Sprintf("/api/worker/%s/claim", c.Node), nil, &job); err != nil {
			if failure := retryController(ctx, err, &attempts); failure != nil {
				return failure
			}
			continue
		}
		if job.Job != nil {
			status, result := runner.ExecuteJob(ctx, *job.Job)
			response := map[string]any{"job_id": job.Job.ID, "lease_token": job.Job.LeaseToken,
				"status": status, "result": result}
			if err := c.request(ctx, "POST", fmt.Sprintf("/api/worker/%s/finish", c.Node), response, nil); err != nil {
				if failure := retryController(ctx, err, &attempts); failure != nil {
					return failure
				}
				continue
			}
			attempts = 0
			continue
		}
		attempts = 0
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Client) executeOperation(parent context.Context, runner *Runner, op Operation) error {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(45 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				claim := map[string]any{"operation_id": op.ID, "lease_token": op.LeaseToken}
				renewCtx, stop := context.WithTimeout(ctx, 20*time.Second)
				err := c.request(renewCtx, "POST", fmt.Sprintf("/api/worker/%s/operation/renew", c.Node), claim, nil)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	output, err := runner.Handle(ctx, op, c)
	cancel()
	<-done
	result := map[string]any{"operation_id": op.ID, "lease_token": op.LeaseToken, "success": err == nil, "result": output}
	if err != nil {
		result["result"] = map[string]any{"error": err.Error()}
	}
	finishCtx, stop := context.WithTimeout(parent, 30*time.Second)
	defer stop()
	return c.request(finishCtx, "POST", fmt.Sprintf("/api/worker/%s/operation/finish", c.Node), result, nil)
}

func (c *Client) Artifact(ctx context.Context, op Operation, id string, w io.Writer) error {
	path := fmt.Sprintf("/api/worker/%s/workspaces/%s/artifacts/%s", c.Node, op.Workspace, id)
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("artifact download HTTP %d", res.StatusCode)
	}
	max := int64(2 * 1024 * 1024 * 1024)
	if v := os.Getenv("PWM_MAX_ARTIFACT_BYTES"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil && n > 0 && n <= 100*1024*1024*1024 {
			max = n
		}
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, hasher), io.LimitReader(res.Body, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return errors.New("artifact exceeds worker download limit")
	}
	expected := res.Header.Get("X-Content-SHA256")
	if expected != "" && hex.EncodeToString(hasher.Sum(nil)) != expected {
		return errors.New("artifact digest mismatch")
	}
	return nil
}

func (c *Client) Upload(ctx context.Context, op Operation, name string, f *os.File, digest string) (string, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}
	path := fmt.Sprintf("/api/worker/%s/workspaces/%s/operations/%s/artifact", c.Node, op.Workspace, op.ID)
	req, err := http.NewRequestWithContext(ctx, "PUT", c.Base+path, f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Operation-Token", op.LeaseToken)
	req.Header.Set("X-Artifact-Name", name)
	req.Header.Set("X-Content-Sha256", digest)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return "", fmt.Errorf("artifact upload HTTP %d: %s", res.StatusCode, string(data))
	}
	var receipt struct {
		ID  string `json:"id"`
		SHA string `json:"sha256"`
	}
	if err := json.NewDecoder(res.Body).Decode(&receipt); err != nil {
		return "", err
	}
	if receipt.SHA != digest || receipt.ID == "" {
		return "", errors.New("artifact upload digest verification failed")
	}
	return receipt.ID, nil
}

// Capabilities reports namespace limits for Kubernetes workers and host
// capacity only for the explicit legacy Docker backend.
func (r *Runner) Capabilities() (map[string]any, error) {
	if r.Kube != nil {
		k := r.Kube
		if !kubernetesName.MatchString(k.Worker) {
			return nil, errors.New("invalid Kubernetes worker identity")
		}
		caps := map[string]any{
			"cpu": k.CPU, "memory_mb": k.MemoryMB, "max_jobs": k.MaxJobs,
			"kubernetes": true, "docker": false, "project_id": k.Project,
			"namespace": k.Namespace, "os": "linux", "arch": runtime.GOARCH,
			"devices": PublicKubernetesDevices(k.Devices), "images": r.ApprovedImages(),
		}
		if k.WorkloadNode != "" {
			caps["workload_node"] = k.WorkloadNode
		}
		return caps, nil
	}
	capabilities, err := Capabilities(r.AvailableDevices())
	if err != nil {
		return nil, err
	}
	capabilities["images"] = r.ApprovedImages()
	return capabilities, nil
}

func Capabilities(devices []Device) (map[string]any, error) {
	mem := int64(0)
	data, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(l, "MemAvailable:") {
				fields := strings.Fields(l)
				if len(fields) >= 2 {
					v, e := strconv.ParseInt(fields[1], 10, 64)
					if e == nil {
						mem = v / 1024
					}
				}
				break
			}
		}
	}
	_, err = exec.LookPath("docker")
	result := map[string]any{"cpu": runtime.NumCPU(), "memory_mb": mem, "max_jobs": 1,
		"docker": runtime.GOOS == "linux" && err == nil, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"devices": PublicDevices(devices)}
	return result, nil
}

func HexSHA(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
