package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dbontr/project-workspace-mcp/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
func pairingCode(flagValue string, fromStdin bool, input io.Reader) (string, error) {
	if fromStdin {
		if flagValue != "" {
			return "", errors.New("provide either --code or --code-stdin, not both")
		}
		data, err := io.ReadAll(io.LimitReader(input, 257))
		if err != nil {
			return "", fmt.Errorf("read pairing code: %w", err)
		}
		if len(data) > 256 {
			return "", errors.New("pairing code input is too long")
		}
		flagValue = strings.TrimSpace(string(data))
	}
	if flagValue == "" {
		return "", errors.New("worker pairing code is required")
	}
	return flagValue, nil
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: project-workspace-worker <doctor|join|serve> [options]")
	}
	cmd := args[0]
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	controller := flags.String("controller", "", "HTTPS controller URL")
	node := flags.String("node", "", "registered worker ID")
	code := flags.String("code", "", "single-use worker pairing code")
	codeStdin := flags.Bool("code-stdin", false, "read the pairing code from standard input")
	tokenFile := flags.String("token-file", "", "worker credential file (owner read-only)")
	deviceFile := flags.String("devices", os.Getenv("PWM_DEVICE_MANIFEST"), "approved host hardware manifest for Docker compatibility")
	kubeDeviceFile := flags.String("kube-devices", os.Getenv("PWM_KUBE_DEVICE_MANIFEST"), "operator-approved Kubernetes device-plugin inventory")
	enableUSB := flags.Bool("enable-usb", os.Getenv("PWM_ENABLE_USB_PASSTHROUGH") == "1", "allow operator-approved USB passthrough (disabled by default)")
	backendDefault := os.Getenv("PWM_EXECUTION_BACKEND")
	if backendDefault == "" {
		backendDefault = "kubernetes"
	}
	backend := flags.String("backend", backendDefault, "executor: kubernetes or docker")
	namespace := flags.String("namespace", os.Getenv("PWM_KUBE_NAMESPACE"), "Kubernetes workload namespace")
	project := flags.String("project", os.Getenv("PWM_KUBE_PROJECT_ID"), "project isolated in this Kubernetes namespace")
	cpu := flags.Float64("capacity-cpu", 4, "advisory namespace CPU capacity")
	memory := flags.Int("capacity-memory-mb", 8192, "advisory namespace memory capacity in MiB")
	maxJobs := flags.Int("max-jobs", 2, "maximum parallel Kubernetes task leases")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if cmd != "doctor" && cmd != "join" && cmd != "serve" {
		return errors.New("unsupported command")
	}
	var devices []worker.Device
	if *backend == "docker" {
		var err error
		devices, err = worker.LoadDevices(*deviceFile)
		if err != nil {
			return err
		}
	} else if *backend != "kubernetes" {
		return errors.New("unsupported execution backend")
	} else if *enableUSB || *deviceFile != "" {
		return errors.New("Kubernetes device claims require an approved device-plugin configuration, not host device mappings")
	}
	runner := worker.NewRunner(devices)
	runner.USBEnabled = *enableUSB && *backend == "docker"
	runner.ReconcileOnConnect = cmd == "serve"
	if *backend == "kubernetes" {
		k, err := worker.NewKubernetes(*namespace, *project, *cpu, *memory, *maxJobs)
		if err != nil {
			return err
		}
		k.Worker = *node
		if cmd == "doctor" && *node == "" {
			k.Worker = "doctor"
		}
		inventory, err := worker.LoadKubernetesDevices(*kubeDeviceFile)
		if err != nil {
			k.Close()
			return err
		}
		k.Devices = inventory
		runner.Kube = k
	}
	if runner.Kube != nil {
		defer runner.Kube.Close()
	}
	capabilities, err := runner.Capabilities()
	if err != nil {
		return err
	}
	if cmd == "doctor" {
		encoded, _ := json.MarshalIndent(capabilities, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`).MatchString(*node) {
		return errors.New("invalid worker ID")
	}
	base, err := worker.ValidateURL(*controller)
	if err != nil {
		return err
	}
	defaultDir := filepath.Join(os.Getenv("HOME"), ".config", "project-workspace", "workers")
	if *tokenFile == "" {
		*tokenFile = filepath.Join(defaultDir, *node+".token")
	}
	c := &worker.Client{Base: base, Node: *node, HTTP: &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 2, IdleConnTimeout: 2 * time.Second, ResponseHeaderTimeout: 120 * time.Second}}}
	if cmd == "join" {
		pairCode, err := pairingCode(*code, *codeStdin, os.Stdin)
		if err != nil {
			return err
		}
		token, err := c.Pair(context.Background(), pairCode, capabilities)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*tokenFile), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(*tokenFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(token + "\n"); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Printf("Worker %s paired; credential stored in %s\n", *node, *tokenFile)
		return nil
	}
	if credential, configured := os.LookupEnv("PWM_WORKER_TOKEN"); configured {
		credential = strings.TrimSuffix(credential, "\n")
		if len(credential) < 32 || strings.ContainsAny(credential, "\x00\r\n\t ") {
			return errors.New("invalid worker credential environment value")
		}
		c.Token = credential
	} else {
		mode, err := os.Stat(*tokenFile)
		if err != nil {
			return err
		}
		if mode.Mode().Perm()&0077 != 0 {
			return errors.New("worker token must not be accessible by other users")
		}
		secret, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		c.Token = strings.TrimSpace(string(secret))
	}
	if c.Token == "" {
		return errors.New("empty worker credential")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.Run(ctx, runner)
}
