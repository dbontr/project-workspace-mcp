package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

func (db *Database) artifactDir() string {
	return filepath.Join(filepath.Dir(db.filename), "artifacts")
}
func (db *Database) artifactPath(id string) string { return filepath.Join(db.artifactDir(), id) }
func (db *Database) Artifacts(project string) ([]map[string]any, error) {
	return db.query("SELECT * FROM artifacts WHERE project=? ORDER BY created DESC", project)
}
func (db *Database) ArtifactInfo(project, id string) (map[string]any, error) {
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		return nil, errors.New("invalid artifact")
	}
	r, e := db.queryOne("SELECT * FROM artifacts WHERE project=? AND id=?", project, id)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, errors.New("artifact not found")
	}
	return r, nil
}
func (db *Database) WorkerArtifactInfo(node, ws, id string) (map[string]any, error) {
	r, e := db.queryOne("SELECT w.project FROM workspaces w JOIN artifact_grants a ON a.workspace=w.id WHERE w.id=? AND w.node=? AND a.artifact=?", ws, node, id)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, errors.New("worker is not authorized for artifact")
	}
	return db.ArtifactInfo(asString(r["project"]), id)
}
func (db *Database) operationUploadAuthorized(node, ws, op, token string) (string, error) {
	r, e := db.queryOne("SELECT w.project,o.action,o.lease_until,o.token_hash FROM operations o JOIN workspaces w ON w.id=o.workspace WHERE o.id=? AND o.workspace=? AND o.node=? AND o.status='leased'", op, ws, node)
	if e != nil {
		return "", e
	}
	if r == nil || asFloat(r["lease_until"]) <= stamp() || !equalSecret(asString(r["token_hash"]), hashToken(token)) {
		return "", errors.New("invalid upload lease")
	}
	switch asString(r["action"]) {
	case "export_file", "snapshot", "export_image", "capture_display":
		return asString(r["project"]), nil
	}
	return "", errors.New("operation does not permit upload")
}
func (db *Database) artifactBytesLocked(project, name string, data []byte) (map[string]any, error) {
	if !validArtifactName.MatchString(name) {
		name = "output"
	}
	if err := os.MkdirAll(db.artifactDir(), 0700); err != nil {
		return nil, err
	}
	id := randomHex(16)
	path := db.artifactPath(id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	meta := map[string]any{"id": id, "name": name, "size": len(data), "sha256": checksum(data)}
	if err = db.execLocked("INSERT INTO artifacts VALUES(?,?,?,?,?,?)", id, project, name, meta["sha256"], len(data), stamp()); err != nil {
		os.Remove(path)
		return nil, err
	}
	return meta, nil
}
func (db *Database) ArtifactSave(project, name string, data []byte) (map[string]any, error) {
	if len(data) > 12<<20 {
		return nil, errors.New("artifact larger than 12 MiB")
	}
	var meta map[string]any
	err := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		var e error
		meta, e = db.artifactBytesLocked(project, name, data)
		return e
	})
	return meta, err
}

// ImportStream writes the entire upload to a temporary file, verifies its digest,
// and moves it into the artifact directory only after project and lease checks.
func (db *Database) ImportStream(project, name, expected string, body io.Reader, maxBytes int64, workerNode, ws, op, leaseToken string) (map[string]any, error) {
	if !validArtifactName.MatchString(name) || !shaPattern.MatchString(expected) {
		return nil, errors.New("invalid artifact metadata")
	}
	if maxBytes < 1 || maxBytes > 100<<30 {
		return nil, errors.New("invalid artifact storage limit")
	}
	if workerNode != "" {
		associated, e := db.operationUploadAuthorized(workerNode, ws, op, leaseToken)
		if e != nil {
			return nil, e
		}
		if associated != project {
			return nil, errors.New("upload project mismatch")
		}
	}
	if err := os.MkdirAll(db.artifactDir(), 0700); err != nil {
		return nil, err
	}
	f, e := os.CreateTemp(db.artifactDir(), ".upload-")
	if e != nil {
		return nil, e
	}
	temp := f.Name()
	defer os.Remove(temp)
	hash := sha256.New()
	size, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(body, maxBytes+1))
	if e != nil {
		f.Close()
		return nil, e
	}
	if size > maxBytes {
		f.Close()
		return nil, errors.New("artifact exceeds size limit")
	}
	if e := f.Sync(); e != nil {
		f.Close()
		return nil, e
	}
	if e := f.Close(); e != nil {
		return nil, e
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != expected {
		return nil, errors.New("artifact SHA-256 mismatch")
	}
	var meta map[string]any
	err := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		if workerNode != "" {
			lease, e := db.validateOperationLeaseLocked(workerNode, op, leaseToken)
			if e != nil {
				return e
			}
			if asString(lease["workspace"]) != ws {
				return errors.New("invalid workspace upload")
			}
			previous, e := db.lockedOne("SELECT 1 existing FROM operation_artifacts WHERE operation=?", op)
			if e != nil {
				return e
			}
			if previous != nil {
				return errors.New("operation already has an artifact")
			}
		}
		id := randomHex(16)
		dest := db.artifactPath(id)
		if e := os.Rename(temp, dest); e != nil {
			return e
		}
		if e := db.execLocked("INSERT INTO artifacts VALUES(?,?,?,?,?,?)", id, project, name, digest, size, stamp()); e != nil {
			os.Remove(dest)
			return e
		}
		if workerNode != "" {
			if e := db.execLocked("INSERT INTO operation_artifacts VALUES(?,?)", op, id); e != nil {
				os.Remove(dest)
				return e
			}
		}
		meta = map[string]any{"id": id, "name": name, "sha256": digest, "size": size}
		return nil
	})
	return meta, err
}
func (db *Database) VerifyArtifact(meta map[string]any) (string, error) {
	id := asString(meta["id"])
	path := db.artifactPath(id)
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	hash := sha256.New()
	count, e := io.Copy(hash, f)
	if e != nil {
		return "", e
	}
	if count != int64(asFloat(meta["size"])) || hex.EncodeToString(hash.Sum(nil)) != asString(meta["sha256"]) {
		return "", fmt.Errorf("artifact %s checksum mismatch", id)
	}
	return path, nil
}

// StoreArtifactStream persists a large archive with a digest calculated during
// transfer, without accumulating its bytes in the controller's address space.
func (db *Database) StoreArtifactStream(project, name string, reader io.Reader, limit int64) (map[string]any, error) {
	if !validArtifactName.MatchString(name) || reader == nil || limit <= 0 || limit > 100<<30 {
		return nil, errors.New("invalid streamed artifact")
	}
	row, e := db.queryOne(`SELECT id FROM projects WHERE id=?`, project)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("project not found")
	}
	if e := os.MkdirAll(db.artifactDir(), 0700); e != nil {
		return nil, e
	}
	tmp, e := os.CreateTemp(db.artifactDir(), ".github-archive-")
	if e != nil {
		return nil, e
	}
	defer os.Remove(tmp.Name())
	hash := sha256.New()
	size, e := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, limit+1))
	if e != nil {
		tmp.Close()
		return nil, e
	}
	if size > limit {
		tmp.Close()
		return nil, errors.New("repository archive exceeds storage limit")
	}
	if e := tmp.Sync(); e != nil {
		tmp.Close()
		return nil, e
	}
	if e := tmp.Close(); e != nil {
		return nil, e
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	var meta map[string]any
	e = db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		id := randomHex(16)
		dest := db.artifactPath(id)
		if e := os.Rename(tmp.Name(), dest); e != nil {
			return e
		}
		if e := db.execLocked(`INSERT INTO artifacts(id,project,name,sha256,size,created) VALUES(?,?,?,?,?,?)`, id, project, name, sha, size, stamp()); e != nil {
			os.Remove(dest)
			return e
		}
		meta = map[string]any{"id": id, "name": name, "sha256": sha, "size": size}
		return nil
	})
	return meta, e
}
