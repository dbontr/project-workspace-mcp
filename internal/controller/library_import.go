package controller

import (
	"errors"
	"os"
)

// QueueLibraryObjectImport grants one workspace access to a shared immutable
// object. A hard link avoids copying multi-gigabyte caches or datasets.
func (db *Database) QueueLibraryObjectImport(project, workspace, library, name, path string) (map[string]any, error) {
	if !validRelativePath(path) {
		return nil, errors.New("invalid workspace destination")
	}
	if !validArtifactName.MatchString(name) {
		return nil, errors.New("invalid library object name")
	}
	var operation, artifactID, digest string
	var size int64
	var linkedPath string
	e := db.transaction(func() error {
		ws, e := db.readyWorkspaceLocked(project, workspace)
		if e != nil {
			return e
		}
		attached, e := db.lockedOne(`SELECT 1 AS present FROM project_libraries WHERE project=? AND library=?`, project, library)
		if e != nil {
			return e
		}
		if attached == nil {
			return errors.New("library not linked to project")
		}
		item, e := db.lockedOne(`SELECT sha256,size FROM library_objects WHERE library=? AND name=?`, library, name)
		if e != nil {
			return e
		}
		if item == nil {
			return errors.New("library object not found")
		}
		digest = asString(item["sha256"])
		size = int64(asFloat(item["size"]))
		if !shaPattern.MatchString(digest) {
			return errors.New("invalid stored object hash")
		}
		id := randomHex(16)
		target := db.artifactPath(id)
		if e := os.Link(db.objectPath(digest), target); e != nil {
			return e
		}
		linkedPath = target
		if e := db.execLocked(`INSERT INTO artifacts(id,project,name,sha256,size,created) VALUES(?,?,?,?,?,?)`, id, project, name, digest, size, stamp()); e != nil {
			return e
		}
		if e := db.execLocked(`INSERT INTO artifact_grants(workspace,artifact) VALUES(?,?)`, workspace, id); e != nil {
			return e
		}
		operation, e = db.EnqueueLocked(workspace, asString(ws["node"]), "import_file", map[string]any{"artifact_id": id, "path": path})
		artifactID = id
		return e
	})
	if e != nil {
		if linkedPath != "" {
			_ = os.Remove(linkedPath)
		}
		return nil, e
	}
	return map[string]any{"operation_id": operation, "artifact_id": artifactID, "sha256": digest, "size": size, "status": "queued"}, nil
}

// QueueImportLibraryRepository checks library membership and the exact pinned
// revision at enqueue time. This does not add general GitHub write access.
func (db *Database) QueueImportLibraryRepository(project, ws, library, repo, sourcePath, revision, artifact string) (map[string]any, error) {
	var id string
	err := db.transaction(func() error {
		workspace, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		linked, e := db.lockedOne(`SELECT 1 AS present FROM project_libraries WHERE project=? AND library=?`, project, library)
		if e != nil {
			return e
		}
		if linked == nil {
			return errors.New("library not linked to project")
		}
		source, e := db.lockedOne(`SELECT revision FROM library_sources WHERE library=? AND repository=? AND path=?`, library, repo, sourcePath)
		if e != nil {
			return e
		}
		if source == nil || asString(source["revision"]) != revision {
			return errors.New("library source revision conflict")
		}
		art, e := db.lockedOne(`SELECT 1 AS present FROM artifacts WHERE project=? AND id=?`, project, artifact)
		if e != nil {
			return e
		}
		if art == nil {
			return errors.New("artifact not found")
		}
		if e := db.execLocked(`INSERT OR IGNORE INTO artifact_grants(workspace,artifact) VALUES(?,?)`, ws, artifact); e != nil {
			return e
		}
		id, e = db.EnqueueLocked(ws, asString(workspace["node"]), "import_archive", map[string]any{"artifact_id": artifact, "repository": repo, "revision": revision})
		return e
	})
	return map[string]any{"operation_id": id, "status": "queued"}, err
}
