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
	"sync"
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
		value = strings.TrimSuffix(value, "\n")
		if len(value) < 32 || strings.ContainsAny(value, "\x00\r\n") {
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

// Capture a baseline on startup. A failed remote backup is retried sooner
// than the normal schedule so a transient outage does not leave new state
// unprotected for an entire backup interval.
func runScheduledOffsiteBackups(ctx context.Context, interval time.Duration,
	backup func(context.Context) (string, error), report func(string, error)) {
	for ctx.Err() == nil {
		backupCtx, cancel := context.WithTimeout(ctx, 12*time.Hour)
		snapshot, err := backup(backupCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		report(snapshot, err)
		delay := interval
		if err != nil && delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func command(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: project-workspace-controller init|controller|pair|backup|restore|backup-offsite|restore-offsite|verify-offsite")
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
	case "backup":
		if len(args) != 2 {
			return errors.New("usage: project-workspace-controller backup DESTINATION_DIRECTORY")
		}
		db, err := controller.OpenDatabase(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.BackupTo(args[1])
	case "restore":
		if len(args) != 2 {
			return errors.New("usage: project-workspace-controller restore BACKUP_DIRECTORY")
		}
		return controller.RestoreBackup(args[1], dbPath)

	case "backup-offsite":
		if len(args) != 1 {
			return errors.New("usage: project-workspace-controller backup-offsite")
		}
		db, err := controller.OpenDatabase(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		// Stop the authoritative controller before using the offline CLI.
		// A running controller uses the in-process interval scheduler instead.
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		id, err := db.BackupOffsite(ctx, controller.ResticConfigFromEnv())
		if err == nil {
			fmt.Printf("Restic snapshot %s uploaded and repository checked\n", id)
		}
		return err
	case "restore-offsite":
		if len(args) != 2 {
			return errors.New("usage: project-workspace-controller restore-offsite SNAPSHOT_ID")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		return controller.RestoreOffsite(ctx, controller.ResticConfigFromEnv(), args[1], dbPath)
	case "verify-offsite":
		if len(args) != 2 {
			return errors.New("usage: project-workspace-controller verify-offsite SNAPSHOT_ID")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		return controller.VerifyOffsite(ctx, controller.ResticConfigFromEnv(), args[1])
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
		var offsite sync.WaitGroup
		if intervalText := os.Getenv("PWM_RESTIC_BACKUP_INTERVAL"); args[0] == "controller" && intervalText != "" {
			interval, err := time.ParseDuration(intervalText)
			if err != nil || interval < time.Hour {
				return errors.New("PWM_RESTIC_BACKUP_INTERVAL must be at least one hour")
			}
			config := controller.ResticConfigFromEnv()
			if err := config.Validate(); err != nil {
				return fmt.Errorf("offsite backup configuration: %w", err)
			}
			offsite.Add(1)
			go func() {
				defer offsite.Done()
				runScheduledOffsiteBackups(ctx, interval, func(backupCtx context.Context) (string, error) {
					return db.BackupOffsite(backupCtx, config)
				}, func(snapshot string, err error) {
					if err != nil {
						log.Printf("scheduled offsite backup failed: %v", err)
					} else {
						log.Printf("scheduled encrypted backup snapshot: %s", snapshot)
					}
				})
			}()
		}
		go func() {
			<-ctx.Done()
			ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			_ = server.Shutdown(ctx)
		}()
		log.Printf("Project Workspace MCP listening at %s", *addr)
		certFile, keyFile := os.Getenv("PWM_TLS_CERT_FILE"), os.Getenv("PWM_TLS_KEY_FILE")
		if (certFile == "") != (keyFile == "") {
			return errors.New("controller HTTPS requires both certificate and private key")
		}
		var serveErr error
		if certFile != "" {
			serveErr = server.ListenAndServeTLS(certFile, keyFile)
		} else {
			serveErr = server.ListenAndServe()
		}
		stop()
		offsite.Wait()
		if serveErr != nil && serveErr != http.ErrServerClosed {
			return serveErr
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
