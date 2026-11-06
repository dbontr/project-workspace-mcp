package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ResticRepositoryConfig delegates encryption and remote storage to Restic.
// Credentials are controller-local and never travel to worker payloads.
type ResticRepositoryConfig struct {
	Repository   string
	PasswordFile string
}

var resticSnapshotID = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

func ResticConfigFromEnv() ResticRepositoryConfig {
	return ResticRepositoryConfig{
		Repository:   os.Getenv("PWM_RESTIC_REPOSITORY"),
		PasswordFile: os.Getenv("PWM_RESTIC_PASSWORD_FILE"),
	}
}

func (c ResticRepositoryConfig) Validate() error { return c.validate() }

func (c ResticRepositoryConfig) validate() error {
	if strings.TrimSpace(c.Repository) == "" || strings.ContainsAny(c.Repository, "\r\n\x00") ||
		!filepath.IsAbs(c.PasswordFile) {
		return errors.New("configure a Restic repository and absolute password file path")
	}
	info, err := os.Lstat(c.PasswordFile)
	if err != nil {
		return fmt.Errorf("Restic password file unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("Restic password file must be a regular, owner-only file")
	}
	if _, err := exec.LookPath("restic"); err != nil {
		return errors.New("Restic executable not found on controller")
	}
	return nil
}

func (c ResticRepositoryConfig) resticCommand(ctx context.Context, directory string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Dir = directory
	// Inherited repository/password settings must not override the explicitly
	// selected repository. Keep cloud-provider auth from the service environment.
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "RESTIC_REPOSITORY", "RESTIC_REPOSITORY_FILE", "RESTIC_PASSWORD",
			"RESTIC_PASSWORD_FILE", "RESTIC_PASSWORD_COMMAND":
			continue
		}
		env = append(env, item)
	}
	cmd.Env = append(env, "RESTIC_REPOSITORY="+c.Repository, "RESTIC_PASSWORD_FILE="+c.PasswordFile)
	cmd.Stderr = io.Discard
	return cmd
}

func (c ResticRepositoryConfig) run(ctx context.Context, directory string, args ...string) error {
	if err := c.validate(); err != nil {
		return err
	}
	cmd := c.resticCommand(ctx, directory, args...)
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Restic %s failed: %w", args[0], err)
	}
	return nil
}

// BackupOffsite captures the existing integrity-checked offline snapshot using
// the same Database instance as the running controller, then sends the bundle
// to an encrypted Restic repository. A separate CLI invocation must run only
// while the authoritative controller process is stopped.
func (db *Database) BackupOffsite(ctx context.Context, c ResticRepositoryConfig) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(db.filename), ".pwm-restic-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := db.BackupTo(filepath.Join(stage, "snapshot")); err != nil {
		return "", fmt.Errorf("snapshot preparation failed: %w", err)
	}
	cmd := c.resticCommand(ctx, stage, "backup", "--json", "--tag", "project-workspace", "snapshot")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("cannot start Restic backup: %w", err)
	}
	var snapshot string
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		var event struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil &&
			event.MessageType == "summary" && resticSnapshotID.MatchString(event.SnapshotID) {
			snapshot = event.SnapshotID
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if scanErr != nil {
		return "", fmt.Errorf("cannot read Restic backup summary: %w", scanErr)
	}
	if waitErr != nil {
		return "", fmt.Errorf("Restic backup failed: %w", waitErr)
	}
	if snapshot == "" {
		return "", errors.New("Restic did not confirm a saved snapshot ID")
	}
	if err := c.run(ctx, stage, "check"); err != nil {
		return "", fmt.Errorf("encrypted repository verification failed for snapshot %s: %w", snapshot, err)
	}
	return snapshot, nil
}

// RestoreOffsite restores an explicitly selected snapshot to an empty data
// directory. It never assumes that 'latest' belongs to the intended owner.
func RestoreOffsite(ctx context.Context, c ResticRepositoryConfig, snapshot, databasePath string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !resticSnapshotID.MatchString(snapshot) {
		return errors.New("an exact Restic snapshot ID is required")
	}
	temp, err := os.MkdirTemp("", ".pwm-restic-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	// Restore the complete snapshot; Restic 0.14 does not support
	// the SNAPSHOT:/SUBDIRECTORY source syntax.
	if err := c.run(ctx, temp, "restore", snapshot, "--target", temp); err != nil {
		return err
	}
	// RestoreBackup checks every manifest hash and refuses to overwrite a
	// database or artifact directory already in use.
	return RestoreBackup(filepath.Join(temp, "snapshot"), databasePath)
}

// VerifyOffsite restores a selected encrypted snapshot into a disposable
// database directory and checks both its manifest and SQLite integrity.
func VerifyOffsite(ctx context.Context, c ResticRepositoryConfig, snapshot string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !resticSnapshotID.MatchString(snapshot) {
		return errors.New("an exact Restic snapshot ID is required")
	}
	if err := c.run(ctx, "", "check", "--read-data"); err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", ".pwm-restic-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	dbpath := filepath.Join(temp, "verified", "state.db")
	if err := RestoreOffsite(ctx, c, snapshot, dbpath); err != nil {
		return err
	}
	db, err := OpenDatabase(dbpath)
	if err != nil {
		return err
	}
	defer db.Close()
	records, err := db.query("PRAGMA integrity_check")
	if err != nil || len(records) != 1 || records[0]["integrity_check"] != "ok" {
		return errors.New("restored SQLite database failed integrity check")
	}
	return nil
}
