package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const mcpUploadChunkBytes = 256 << 10
const mcpUploadLifetimeSeconds = 3600

// MCP uploads are durable, bounded, owner-scoped sessions. A session does not
// publish anything until all bytes and the declared SHA-256 have been verified.
func (db *Database) mcpUploadDir() string {
	return filepath.Join(filepath.Dir(db.filename), "mcp-uploads")
}

func (db *Database) mcpUploadPath(id string) string {
	return filepath.Join(db.mcpUploadDir(), id+".part")
}

func (db *Database) reapMCPUploads() {
	rows, err := db.query("SELECT id FROM mcp_upload_sessions WHERE expires < ?", stamp())
	if err != nil {
		return
	}
	for _, row := range rows {
		id := asString(row["id"])
		if !contributionArtifactID.MatchString(id) {
			continue
		}
		if db.exec("DELETE FROM mcp_upload_sessions WHERE id=? AND expires<?", id, stamp()) == nil {
			_ = os.Remove(db.mcpUploadPath(id))
		}
	}
}

func uploadScope(project, task string) (string, string, error) {
	if (project == "") == (task == "") {
		return "", "", errors.New("specify exactly one upload project or personal task")
	}
	if project != "" {
		if !validID.MatchString(project) {
			return "", "", errors.New("invalid upload project")
		}
		return "project", project, nil
	}
	if !contributionArtifactID.MatchString(task) {
		return "", "", errors.New("invalid upload personal task")
	}
	return "task", task, nil
}

func (db *Database) BeginMCPUpload(owner, project, task, name, digest string,
	size int64, expectedRevision int, limit int64) (map[string]any, error) {
	scope, target, err := uploadScope(project, task)
	if err != nil || !validTaskOwner(owner) || !validArtifactName.MatchString(name) ||
		!shaPattern.MatchString(digest) || size < 0 || size > limit || limit < 1 ||
		limit > 100<<30 || expectedRevision < 0 {
		return nil, errors.New("invalid MCP upload metadata or size")
	}
	if scope == "project" && expectedRevision != 0 {
		return nil, errors.New("project upload does not accept a file revision")
	}
	db.reapMCPUploads()
	if err := os.MkdirAll(db.mcpUploadDir(), 0700); err != nil {
		return nil, err
	}
	id := randomHex(16)
	f, err := os.OpenFile(db.mcpUploadPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(db.mcpUploadPath(id))
		return nil, err
	}
	created, expires := stamp(), stamp()+mcpUploadLifetimeSeconds
	err = db.transaction(func() error {
		active, err := db.countLocked(
			"SELECT count(*) AS count FROM mcp_upload_sessions WHERE owner=? AND state!='complete'",
			owner)
		if err != nil {
			return err
		}
		if active >= 8 {
			return errors.New("too many incomplete uploads for owner")
		}
		if scope == "project" {
			if err := requireProject(db, target); err != nil {
				return err
			}
		} else {
			item, err := db.lockedOne(
				"SELECT status FROM personal_tasks WHERE id=? AND owner=?", target, owner)
			if err != nil {
				return err
			}
			if item == nil || asString(item["status"]) != "active" {
				return errors.New("personal task not found or archived")
			}
		}
		return db.execLocked(
			"INSERT INTO mcp_upload_sessions(id,owner,scope,target,name,digest,size,revision,received,state,result,created,expires) VALUES(?,?,?,?,?,?,?,?,0,'open','',?,?)",
			id, owner, scope, target, name, digest, size, expectedRevision, created, expires)
	})
	if err != nil {
		_ = os.Remove(db.mcpUploadPath(id))
		return nil, err
	}
	return map[string]any{
		"upload_id": id, "scope": scope, "target": target, "name": name,
		"sha256": digest, "size_bytes": size, "received_bytes": int64(0),
		"chunk_bytes": mcpUploadChunkBytes, "expires": expires,
	}, nil
}

func (db *Database) mcpUploadRow(owner, id, project, task string) (map[string]any, error) {
	scope, target, err := uploadScope(project, task)
	if err != nil || !validTaskOwner(owner) || !contributionArtifactID.MatchString(id) {
		return nil, errors.New("invalid upload identity")
	}
	row, err := db.queryOne("SELECT * FROM mcp_upload_sessions WHERE id=? AND owner=? AND scope=? AND target=?",
		id, owner, scope, target)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("upload session not found")
	}
	if asFloat(row["expires"]) <= stamp() {
		return nil, errors.New("upload session expired")
	}
	return row, nil
}

func mcpUploadProgress(row map[string]any) map[string]any {
	progress := map[string]any{
		"upload_id": row["id"], "scope": row["scope"], "target": row["target"],
		"name": row["name"], "size_bytes": row["size"],
		"received_bytes": row["received"], "state": row["state"],
		"expires": row["expires"], "sha256": row["digest"],
	}
	if asString(row["state"]) == "complete" {
		progress["result"] = decodeObject(asString(row["result"]))
	}
	return progress
}

func (db *Database) MCPUploadStatus(owner, id, project, task string) (map[string]any, error) {
	row, err := db.mcpUploadRow(owner, id, project, task)
	if err != nil {
		return nil, err
	}
	return mcpUploadProgress(row), nil
}

func (db *Database) AppendMCPUpload(owner, id, project, task string,
	offset int64, encoded string) (map[string]any, error) {
	scope, target, err := uploadScope(project, task)
	if err != nil || !contributionArtifactID.MatchString(id) || !validTaskOwner(owner) ||
		offset < 0 || len(encoded) == 0 ||
		len(encoded) > base64.StdEncoding.EncodedLen(mcpUploadChunkBytes) {
		return nil, errors.New("invalid upload chunk")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > mcpUploadChunkBytes {
		return nil, errors.New("invalid base64 upload chunk")
	}
	var progress map[string]any
	err = db.transaction(func() error {
		row, err := db.lockedOne(
			"SELECT * FROM mcp_upload_sessions WHERE id=? AND owner=? AND scope=? AND target=?",
			id, owner, scope, target)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("upload session not found")
		}
		if asString(row["state"]) != "open" || asFloat(row["expires"]) <= stamp() {
			return errors.New("upload session is not open")
		}
		received, size := int64(asFloat(row["received"])), int64(asFloat(row["size"]))
		if offset != received {
			return fmt.Errorf("upload offset conflict: received %d, supplied %d", received, offset)
		}
		if int64(len(data)) > size-received {
			return errors.New("upload exceeds declared file size")
		}
		path := db.mcpUploadPath(id)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("upload backing file missing or unsafe")
		}
		if info.Size() < received {
			return errors.New("upload backing file is truncated")
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = file.Truncate(received); err == nil {
			_, err = file.Seek(received, io.SeekStart)
		}
		if err == nil {
			var written int
			written, err = file.Write(data)
			if err == nil && written != len(data) {
				err = io.ErrShortWrite
			}
		}
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Truncate(path, received)
			return err
		}
		next := received + int64(len(data))
		if err = db.execLocked(
			"UPDATE mcp_upload_sessions SET received=? WHERE id=? AND received=? AND state='open'",
			next, id, received); err != nil {
			_ = os.Truncate(path, received)
			return err
		}
		row["received"] = next
		progress = mcpUploadProgress(row)
		return nil
	})
	return progress, err
}

// FinishMCPUpload publishes bytes and session completion in one SQL
// transaction. A retry either finds the open upload or the committed result.
func (db *Database) FinishMCPUpload(owner, id, project, task string, limit int64) (map[string]any, error) {
	row, err := db.mcpUploadRow(owner, id, project, task)
	if err != nil {
		return nil, err
	}
	if asString(row["state"]) == "complete" {
		return decodeObject(asString(row["result"])), nil
	}
	size := int64(asFloat(row["size"]))
	if asString(row["state"]) != "open" || size > limit ||
		int64(asFloat(row["received"])) != size {
		return nil, errors.New("upload is not ready for completion")
	}
	path := db.mcpUploadPath(id)
	source, err := os.Open(path)
	if err != nil {
		// A concurrent finalize can commit and remove the staging file.
		if latest, lookupErr := db.mcpUploadRow(owner, id, project, task); lookupErr == nil &&
			asString(latest["state"]) == "complete" {
			return decodeObject(asString(latest["result"])), nil
		}
		return nil, err
	}
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		source.Close()
		return nil, errors.New("upload backing file size mismatch")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(source, limit+1))
	closeErr := source.Close()
	if err != nil || closeErr != nil || count != size ||
		hex.EncodeToString(hash.Sum(nil)) != asString(row["digest"]) {
		return nil, errors.New("upload SHA-256 or size mismatch")
	}

	if err := os.MkdirAll(db.artifactDir(), 0700); err != nil {
		return nil, err
	}
	var result map[string]any
	var destination string
	err = db.transaction(func() error {
		latest, err := db.lockedOne(
			"SELECT * FROM mcp_upload_sessions WHERE id=? AND owner=? AND scope=? AND target=?",
			id, owner, row["scope"], row["target"])
		if err != nil {
			return err
		}
		if latest == nil || asFloat(latest["expires"]) <= stamp() {
			return errors.New("upload session expired or unavailable")
		}
		if asString(latest["state"]) == "complete" {
			result = decodeObject(asString(latest["result"]))
			return nil
		}
		if asString(latest["state"]) != "open" ||
			int64(asFloat(latest["received"])) != size {
			return errors.New("upload completion conflict")
		}
		checked, err := os.Lstat(path)
		if err != nil || !checked.Mode().IsRegular() || checked.Size() != size ||
			!os.SameFile(info, checked) || !info.ModTime().Equal(checked.ModTime()) {
			return errors.New("upload backing file changed during verification")
		}
		scope := asString(row["scope"])
		target := asString(row["target"])
		now := stamp()
		if scope == "task" {
			taskRow, err := db.lockedOne(
				"SELECT status FROM personal_tasks WHERE id=? AND owner=?", target, owner)
			if err != nil {
				return err
			}
			if taskRow == nil || asString(taskRow["status"]) != "active" {
				return errors.New("personal task not found or archived")
			}
			last, err := db.lockedOne(
				"SELECT coalesce(max(revision),0) revision FROM personal_task_files WHERE task=? AND name=?",
				target, row["name"])
			if err != nil {
				return err
			}
			if asInt(last["revision"]) != asInt(row["revision"]) {
				return errors.New("task file revision conflict")
			}
		} else if err := requireProject(db, target); err != nil {
			return err
		}

		artifactID := randomHex(16)
		destination = db.artifactPath(artifactID)
		if err := os.Link(path, destination); err != nil {
			destination = ""
			return err
		}
		artifactProject := target
		if scope == "task" {
			artifactProject = taskFileScope(target)
		}
		if err := db.execLocked("INSERT INTO artifacts VALUES(?,?,?,?,?,?)",
			artifactID, artifactProject, row["name"], row["digest"], size, now); err != nil {
			return err
		}
		if scope == "task" {
			revision := asInt(row["revision"]) + 1
			if err := db.execLocked(
				"INSERT INTO personal_task_files(task,name,revision,artifact_id,sha256,size,created) VALUES(?,?,?,?,?,?,?)",
				target, row["name"], revision, artifactID, row["digest"], size, now); err != nil {
				return err
			}
			result = map[string]any{
				"task_id": target, "name": row["name"], "revision": revision,
				"artifact_id": artifactID, "sha256": row["digest"], "size": size, "created": now,
			}
		} else {
			result = map[string]any{
				"id": artifactID, "name": row["name"], "sha256": row["digest"], "size": size,
			}
		}
		return db.execLocked(
			"UPDATE mcp_upload_sessions SET state='complete',result=? WHERE id=? AND owner=? AND state='open'",
			encode(result), id, owner)
	})
	if err != nil {
		if destination != "" {
			_ = os.Remove(destination)
		}
		return nil, err
	}
	_ = os.Remove(path)
	return result, nil
}

func (db *Database) CancelMCPUpload(owner, id, project, task string) (map[string]any, error) {
	scope, target, err := uploadScope(project, task)
	if err != nil || !contributionArtifactID.MatchString(id) || !validTaskOwner(owner) {
		return nil, errors.New("invalid upload identity")
	}
	err = db.transaction(func() error {
		row, err := db.lockedOne(
			"SELECT state,expires FROM mcp_upload_sessions WHERE id=? AND owner=? AND scope=? AND target=?",
			id, owner, scope, target)
		if err != nil {
			return err
		}
		if row == nil || asFloat(row["expires"]) <= stamp() {
			return errors.New("upload session expired or unavailable")
		}
		if asString(row["state"]) != "open" {
			return errors.New("completed uploads cannot be cancelled")
		}
		return db.execLocked(
			"DELETE FROM mcp_upload_sessions WHERE id=? AND owner=? AND scope=? AND target=?",
			id, owner, scope, target)
	})
	if err != nil {
		return nil, err
	}
	_ = os.Remove(db.mcpUploadPath(id))
	return map[string]any{"upload_id": id, "status": "cancelled"}, nil
}
