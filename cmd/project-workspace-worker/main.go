package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
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
	tokenFile := flags.String("token-file", "", "worker credential file (owner read-only)")
	deviceFile := flags.String("devices", os.Getenv("PWM_DEVICE_MANIFEST"), "approved hardware manifest")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if cmd != "doctor" && cmd != "join" && cmd != "serve" {
		return errors.New("unsupported command")
	}
	devices, err := worker.LoadDevices(*deviceFile)
	if err != nil {
		return err
	}
	capabilities, err := worker.Capabilities(devices)
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
		if *code == "" {
			return errors.New("worker pairing code is required")
		}
		token, err := c.Pair(context.Background(), *code, capabilities)
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
	if c.Token == "" {
		return errors.New("empty worker credential")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.Run(ctx, worker.NewRunner(devices))
}
