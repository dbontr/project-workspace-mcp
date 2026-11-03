package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dbontr/project-workspace-mcp/internal/controller"
)

func location() (string, string) {
	home, _ := os.UserHomeDir()
	data := filepath.Join(home, ".local", "share", "project-workspace", "state.db")
	if path := os.Getenv("PWM_DB"); path != "" {
		data = path
	}
	secret := filepath.Join(home, ".config", "project-workspace", "admin.token")
	return data, secret
}
func readToken(path string) (string, error) {
	if value := os.Getenv("PWM_ADMIN_TOKEN"); value != "" {
		if len(value) < 32 {
			return "", errors.New("PWM_ADMIN_TOKEN is too short")
		}
		return value, nil
	}
	info, e := os.Stat(path)
	if e != nil {
		return "", e
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("administrator token must have owner-only permissions")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", errors.New("administrator token is too short")
	}
	return token, nil
}
func command(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: project-workspace-controller init|controller|pair")
	}
	dbPath, tokenFile := location()
	switch args[0] {
	case "init":
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
			return err
		}
		file, e := os.OpenFile(tokenFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		token := controller.RandomCredential()
		if _, e := fmt.Fprintln(file, token); e != nil {
			file.Close()
			return e
		}
		if e := file.Close(); e != nil {
			return e
		}
		fmt.Printf("Administrator credential created at %s\n", tokenFile)
		return nil
	case "pair", "controller":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		dbFile := flags.String("db", dbPath, "SQLite project database")
		addr := flags.String("listen", "127.0.0.1:8000", "controller listen address")
		if e := flags.Parse(args[1:]); e != nil {
			return e
		}
		token, e := readToken(tokenFile)
		if e != nil {
			return fmt.Errorf("controller initialization required: %w", e)
		}
		db, e := controller.OpenDatabase(*dbFile)
		if e != nil {
			return e
		}
		defer db.Close()
		if args[0] == "pair" {
			v, e := db.PairingCode()
			if e != nil {
				return e
			}
			fmt.Printf("Pairing code (10 min): %s\n", v["pairing_code"])
			return nil
		}
		service, e := controller.NewServer(db, token)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") && os.Getenv("PWM_ALLOW_REMOTE_LISTEN") != "1" {
			return errors.New("remote bind requires PWM_ALLOW_REMOTE_LISTEN=1 and TLS authentication at the ingress")
		}
		server := &http.Server{Addr: *addr, Handler: service, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 16, IdleTimeout: 120 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			_ = server.Shutdown(ctx)
		}()
		log.Printf("Project Workspace MCP listening at %s", *addr)
		if e := server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			return e
		}
		return nil
	}
	return errors.New("unknown controller command")
}
func main() {
	if err := command(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
