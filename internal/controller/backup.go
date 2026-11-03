package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type backupEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type backupManifest struct {
	Version int           `json:"version"`
	Files   []backupEntry `json:"files"`
}

func hashFile(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("backup source is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func copyVerified(src, dst, expected string, size int64) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("backup source size or type mismatch: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != expected {
		return fmt.Errorf("backup content mismatch: %s", src)
	}
	return nil
}

func backupPathAllowed(path string) bool {
	if path == "state.db" {
		return true
	}
	if !strings.HasPrefix(path, "artifacts/") || !validRelativePath(path) {
		return false
	}
	p := strings.Split(path, "/")
	if len(p) == 2 {
		return len(p[1]) == 32 && strings.Trim(p[1], "0123456789abcdef") == ""
	}
	return len(p) == 4 && p[1] == "objects" && len(p[2]) == 2 &&
		len(p[3]) == 64 && strings.HasPrefix(p[3], p[2]) &&
		strings.Trim(p[3], "0123456789abcdef") == ""
}

// BackupTo captures the database and referenced immutable artifacts. Call it
// only when no other controller process is writing to the same data directory.
func (db *Database) BackupTo(destination string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if _, err := os.Lstat(destination); err == nil {
		return errors.New("backup destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	uploads, err := db.queryLocked("SELECT id FROM library_uploads LIMIT 1")
	if err != nil {
		return err
	}
	if len(uploads) != 0 {
		return errors.New("cannot back up while library uploads are pending")
	}
	artifacts, err := db.queryLocked("SELECT id,sha256,size FROM artifacts")
	if err != nil {
		return err
	}
	objects, err := db.queryLocked("SELECT DISTINCT sha256,size FROM library_objects")
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(destination), ".pwm-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	// VACUUM INTO produces a consistent SQLite snapshot, including WAL changes.
	snapshot := filepath.Join(temp, "state.db")
	quoted := strings.ReplaceAll(snapshot, "'", "''")
	if err := db.execLocked("VACUUM INTO '" + quoted + "'"); err != nil {
		return fmt.Errorf("SQLite snapshot: %w", err)
	}
	manifest := backupManifest{Version: 1}
	add := func(relative, hash string, size int64) error {
		if !backupPathAllowed(relative) || !shaPattern.MatchString(hash) || size < 0 {
			return errors.New("invalid backup record")
		}
		source := filepath.Join(filepath.Dir(db.filename), relative)
		if err := copyVerified(source, filepath.Join(temp, relative), hash, size); err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, backupEntry{Path: relative, SHA256: hash, Size: size})
		return nil
	}
	for _, row := range artifacts {
		id, hash := asString(row["id"]), asString(row["sha256"])
		size := int64(asFloat(row["size"]))
		if err := add(filepath.Join("artifacts", id), hash, size); err != nil {
			return err
		}
	}
	for _, row := range objects {
		hash := asString(row["sha256"])
		size := int64(asFloat(row["size"]))
		if !shaPattern.MatchString(hash) {
			return errors.New("invalid library object hash")
		}
		if err := add(filepath.Join("artifacts", "objects", hash[:2], hash), hash, size); err != nil {
			return err
		}
	}
	hash, size, err := hashFile(snapshot)
	if err != nil {
		return err
	}
	manifest.Files = append(manifest.Files, backupEntry{Path: "state.db", SHA256: hash, Size: size})
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, "manifest.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temp, destination)
}

// RestoreBackup verifies the complete snapshot before creating a new data store.
// It never replaces an existing database or artifact directory.
func RestoreBackup(source, databasePath string) error {
	raw, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest backupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if manifest.Version != 1 || len(manifest.Files) == 0 {
		return errors.New("unsupported or empty backup manifest")
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if !backupPathAllowed(file.Path) || seen[file.Path] || !shaPattern.MatchString(file.SHA256) || file.Size < 0 {
			return errors.New("invalid backup manifest entry")
		}
		seen[file.Path] = true
		hash, size, err := hashFile(filepath.Join(source, file.Path))
		if err != nil {
			return err
		}
		if hash != file.SHA256 || size != file.Size {
			return fmt.Errorf("backup integrity failure: %s", file.Path)
		}
	}
	if !seen["state.db"] {
		return errors.New("backup database missing")
	}
	if _, err := os.Lstat(databasePath); err == nil {
		return errors.New("restore database already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	artifactDir := filepath.Join(filepath.Dir(databasePath), "artifacts")
	if _, err := os.Lstat(artifactDir); err == nil {
		return errors.New("restore artifact directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(databasePath)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".pwm-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for _, file := range manifest.Files {
		if err := copyVerified(filepath.Join(source, file.Path), filepath.Join(temp, file.Path), file.SHA256, file.Size); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(temp, "artifacts"), 0700); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(temp, "artifacts"), artifactDir); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(temp, "state.db"), databasePath); err != nil {
		os.RemoveAll(artifactDir)
		return err
	}
	return nil
}
