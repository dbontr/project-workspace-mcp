package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

var personalTagName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func validTaskOwner(owner string) bool {
	if owner == "controller-owner" || validPrincipal(owner) {
		return true
	}
	if !strings.HasPrefix(owner, "cloudflare-access:https://") || len(owner) > 1024 ||
		strings.ContainsAny(owner, "\x00\r\n") {
		return false
	}
	identity := strings.TrimPrefix(owner, "cloudflare-access:")
	separator := strings.LastIndexByte(identity, '#')
	return separator > len("https://") && separator+1 < len(identity) &&
		strings.Contains(identity[separator+1:], "@")
}

func taskFileScope(task string) string { return "personal-task:" + task }

func validatePersonalTask(title, description string, tags []string, status string) error {
	if strings.TrimSpace(title) == "" || len(title) > 200 || len(description) > 4096 ||
		(status != "active" && status != "archived") || len(tags) > 16 {
		return errors.New("invalid personal task metadata")
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if !personalTagName.MatchString(tag) || seen[tag] {
			return errors.New("invalid or duplicate task tag")
		}
		seen[tag] = true
	}
	return nil
}

func (db *Database) CreatePersonalTask(owner, title, description string, tags []string) (map[string]any, error) {
	if !validTaskOwner(owner) {
		return nil, errors.New("invalid task owner")
	}
	if err := validatePersonalTask(title, description, tags, "active"); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}
	id, now := randomHex(16), stamp()
	err = db.exec("INSERT INTO personal_tasks(id,owner,title,description,tags,status,version,created,updated) VALUES(?,?,?,?,?,'active',1,?,?)",
		id, owner, title, description, string(raw), now, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "title": title, "description": description, "tags": tags,
		"status": "active", "version": 1, "created": now}, nil
}

func (db *Database) PersonalTasks(owner, search string) ([]map[string]any, error) {
	if !validTaskOwner(owner) || len(search) > 256 {
		return nil, errors.New("invalid task search")
	}
	match := "%" + search + "%"
	rows, err := db.query(`SELECT id,title,description,tags,status,version,created,updated
		FROM personal_tasks WHERE owner=? AND
		(title LIKE ? OR description LIKE ? OR tags LIKE ?) ORDER BY updated DESC,id DESC LIMIT 200`,
		owner, match, match, match)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var tags []string
		if err := json.Unmarshal([]byte(asString(row["tags"])), &tags); err != nil {
			return nil, err
		}
		row["tags"] = tags
	}
	return rows, nil
}

func (db *Database) PersonalTaskInfo(owner, task string) (map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) {
		return nil, errors.New("invalid task")
	}
	row, err := db.queryOne("SELECT id,title,description,tags,status,version,created,updated FROM personal_tasks WHERE id=? AND owner=?", task, owner)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("personal task not found")
	}
	var tags []string
	if err := json.Unmarshal([]byte(asString(row["tags"])), &tags); err != nil {
		return nil, err
	}
	row["tags"] = tags
	return row, nil
}

// UpdatePersonalTask guards metadata changes by exact revision and never removes
// source files or earlier task revisions.
func (db *Database) UpdatePersonalTask(owner, task, title, description, status string, tags []string, expected int) (map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) || expected < 1 {
		return nil, errors.New("invalid task update")
	}
	if err := validatePersonalTask(title, description, tags, status); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}
	err = db.transaction(func() error {
		row, err := db.lockedOne("SELECT version FROM personal_tasks WHERE id=? AND owner=?", task, owner)
		if err != nil {
			return err
		}
		if row == nil || asInt(row["version"]) != expected {
			return errors.New("task revision conflict or unavailable task")
		}
		return db.execLocked("UPDATE personal_tasks SET title=?,description=?,tags=?,status=?,version=?,updated=? WHERE id=? AND owner=?",
			title, description, string(raw), status, expected+1, stamp(), task, owner)
	})
	if err != nil {
		return nil, err
	}
	return db.PersonalTaskInfo(owner, task)
}

func (db *Database) PersonalTaskFiles(owner, task string) ([]map[string]any, error) {
	if _, err := db.PersonalTaskInfo(owner, task); err != nil {
		return nil, err
	}
	return db.query(`SELECT name,revision,artifact_id,sha256,size,created FROM personal_task_files
		WHERE task=? ORDER BY name,revision DESC LIMIT 1000`, task)
}

func (db *Database) PersonalTaskFile(owner, task, name string, revision int) (map[string]any, error) {
	if !validArtifactName.MatchString(name) || revision < 0 {
		return nil, errors.New("invalid task filename or revision")
	}
	if _, err := db.PersonalTaskInfo(owner, task); err != nil {
		return nil, err
	}
	var row map[string]any
	var err error
	if revision == 0 {
		row, err = db.queryOne(`SELECT name,revision,artifact_id,sha256,size,created FROM personal_task_files
			WHERE task=? AND name=? ORDER BY revision DESC LIMIT 1`, task, name)
	} else {
		row, err = db.queryOne(`SELECT name,revision,artifact_id,sha256,size,created FROM personal_task_files
			WHERE task=? AND name=? AND revision=?`, task, name, revision)
	}
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("task file revision not found")
	}
	row["task_id"] = task
	return row, nil
}

func (db *Database) OpenPersonalTaskFile(owner, task, name string, revision int) (*os.File, map[string]any, error) {
	record, err := db.PersonalTaskFile(owner, task, name, revision)
	if err != nil {
		return nil, nil, err
	}
	meta, err := db.ArtifactInfo(taskFileScope(task), asString(record["artifact_id"]))
	if err != nil {
		return nil, nil, err
	}
	path, err := db.VerifyArtifact(meta)
	if err != nil || asString(meta["sha256"]) != asString(record["sha256"]) ||
		asFloat(meta["size"]) != asFloat(record["size"]) {
		return nil, nil, errors.New("task file integrity mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, record, nil
}

func (db *Database) PersonalTaskFileBase64(owner, task, name string, revision int) (map[string]any, error) {
	record, err := db.PersonalTaskFile(owner, task, name, revision)
	if err != nil {
		return nil, err
	}
	if asFloat(record["size"]) > maxMCPArtifactBytes {
		return nil, errors.New("task file is larger than MCP inline transfer limit; use authenticated REST download")
	}
	file, record, err := db.OpenPersonalTaskFile(owner, task, name, revision)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMCPArtifactBytes+1))
	if err != nil || len(data) > maxMCPArtifactBytes {
		return nil, errors.New("invalid inline task file length")
	}
	record["data_base64"] = base64.StdEncoding.EncodeToString(data)
	return record, nil
}

// SavePersonalTaskStream preserves all bytes and verifies the requested digest
// before committing a new immutable, compare-and-swap file revision. Task files
// reuse the controller's verified artifact store, including backup manifests.
func (db *Database) SavePersonalTaskStream(owner, task, name, expectedDigest string, input io.Reader, maxBytes int64, expectedRevision int) (map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) ||
		!validArtifactName.MatchString(name) || !shaPattern.MatchString(expectedDigest) ||
		input == nil || expectedRevision < 0 || maxBytes < 1 || maxBytes > 100<<30 {
		return nil, errors.New("invalid task upload")
	}
	if err := os.MkdirAll(db.artifactDir(), 0700); err != nil {
		return nil, err
	}
	tempFile, err := os.CreateTemp(db.artifactDir(), ".personal-upload-")
	if err != nil {
		return nil, err
	}
	temp := tempFile.Name()
	defer os.Remove(temp)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tempFile, hash), io.LimitReader(input, maxBytes+1))
	if err != nil {
		tempFile.Close()
		return nil, err
	}
	if size > maxBytes {
		tempFile.Close()
		return nil, errors.New("personal task upload exceeds configured size limit")
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return nil, err
	}
	if err := tempFile.Close(); err != nil {
		return nil, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != expectedDigest {
		return nil, errors.New("personal task file checksum mismatch")
	}
	var meta map[string]any
	err = db.transaction(func() error {
		row, err := db.lockedOne("SELECT status FROM personal_tasks WHERE id=? AND owner=?", task, owner)
		if err != nil {
			return err
		}
		if row == nil || asString(row["status"]) != "active" {
			return errors.New("personal task not found or archived")
		}
		last, err := db.lockedOne("SELECT coalesce(max(revision),0) revision FROM personal_task_files WHERE task=? AND name=?", task, name)
		if err != nil {
			return err
		}
		if asInt(last["revision"]) != expectedRevision {
			return errors.New("task file revision conflict")
		}
		id, now := randomHex(16), stamp()
		dst := db.artifactPath(id)
		if err := os.Link(temp, dst); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO artifacts VALUES(?,?,?,?,?,?)",
			id, taskFileScope(task), name, digest, size, now); err != nil {
			os.Remove(dst)
			return err
		}
		if err := db.execLocked(`INSERT INTO personal_task_files(task,name,revision,artifact_id,sha256,size,created)
			VALUES(?,?,?,?,?,?,?)`, task, name, expectedRevision+1, id, digest, size, now); err != nil {
			os.Remove(dst)
			return err
		}
		meta = map[string]any{"task_id": task, "name": name, "revision": expectedRevision + 1,
			"artifact_id": id, "sha256": digest, "size": size, "created": now}
		return nil
	})
	return meta, err
}

func (db *Database) SavePersonalTaskText(owner, task, name, body string, expectedRevision int) (map[string]any, error) {
	if len(body) > maxMCPArtifactBytes {
		return nil, errors.New("task text exceeds MCP inline transfer limit")
	}
	return db.SavePersonalTaskStream(owner, task, name, checksum([]byte(body)),
		strings.NewReader(body), maxMCPArtifactBytes, expectedRevision)
}

func (db *Database) SavePersonalTaskBase64(owner, task, name, digest, data string, expectedRevision int) (map[string]any, error) {
	if len(data) > base64.StdEncoding.EncodedLen(maxMCPArtifactBytes) {
		return nil, errors.New("task upload exceeds inline transfer limit")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(raw) > maxMCPArtifactBytes {
		return nil, errors.New("invalid base64 task data")
	}
	return db.SavePersonalTaskStream(owner, task, name, digest,
		bytes.NewReader(raw), maxMCPArtifactBytes, expectedRevision)
}

// PromotePersonalTaskFile creates a project-owned immutable artifact from a
// task file. The caller must separately prove project editor authorization.
func (db *Database) PromotePersonalTaskFile(owner, task, name string, revision int, project string) (map[string]any, error) {
	file, record, err := db.OpenPersonalTaskFile(owner, task, name, revision)
	if err != nil {
		return nil, err
	}
	file.Close()
	if !validID.MatchString(project) {
		return nil, errors.New("invalid promotion destination")
	}
	var result map[string]any
	err = db.transaction(func() error {
		if err := requireProject(db, project); err != nil {
			return err
		}
		// Recheck ownership inside the transaction before granting a project a
		// new durable artifact, even when the source task is archived.
		t, err := db.lockedOne("SELECT id FROM personal_tasks WHERE id=? AND owner=?", task, owner)
		if err != nil || t == nil {
			return errors.New("personal task not found")
		}
		id, now := randomHex(16), stamp()
		if err := os.Link(db.artifactPath(asString(record["artifact_id"])), db.artifactPath(id)); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO artifacts VALUES(?,?,?,?,?,?)", id, project, name,
			record["sha256"], int64(asFloat(record["size"])), now); err != nil {
			os.Remove(db.artifactPath(id))
			return err
		}
		result = map[string]any{"project": project, "id": id, "name": name,
			"sha256": record["sha256"], "size": record["size"],
			"source_task": task, "source_revision": record["revision"]}
		return nil
	})
	return result, err
}
