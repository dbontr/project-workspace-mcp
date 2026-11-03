package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY,title TEXT NOT NULL,created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS notes(project TEXT NOT NULL,name TEXT NOT NULL,body TEXT NOT NULL,version INTEGER NOT NULL DEFAULT 1,updated REAL NOT NULL,PRIMARY KEY(project,name),FOREIGN KEY(project) REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS repositories(project TEXT NOT NULL,full_name TEXT NOT NULL,PRIMARY KEY(project,full_name));
CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY,project TEXT NOT NULL,name TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL,capabilities TEXT NOT NULL,last_seen REAL NOT NULL);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,project TEXT NOT NULL,node TEXT,command TEXT NOT NULL,image TEXT NOT NULL,cpu REAL NOT NULL,memory_mb INTEGER NOT NULL,status TEXT NOT NULL,lease_until REAL,token_hash TEXT,created REAL NOT NULL,updated REAL NOT NULL,result TEXT);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status,created);
CREATE TABLE IF NOT EXISTS pairings(code_hash TEXT PRIMARY KEY,expires REAL NOT NULL);
CREATE TABLE IF NOT EXISTS workspaces(id TEXT PRIMARY KEY,project TEXT NOT NULL,node TEXT NOT NULL,image TEXT NOT NULL,cpu REAL NOT NULL,memory_mb INTEGER NOT NULL,status TEXT NOT NULL,expires REAL NOT NULL,created REAL NOT NULL,updated REAL NOT NULL,FOREIGN KEY(project) REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,workspace TEXT NOT NULL,node TEXT NOT NULL,action TEXT NOT NULL,payload TEXT NOT NULL,status TEXT NOT NULL,token_hash TEXT,lease_until REAL,result TEXT,created REAL NOT NULL,updated REAL NOT NULL,FOREIGN KEY(workspace) REFERENCES workspaces(id));
CREATE INDEX IF NOT EXISTS idx_operations_node ON operations(node,status,created);
CREATE TABLE IF NOT EXISTS hardware_leases(workspace TEXT NOT NULL,node TEXT NOT NULL,device_id TEXT NOT NULL,kind TEXT NOT NULL,acquired REAL NOT NULL,released REAL,PRIMARY KEY(workspace,node,device_id),FOREIGN KEY(workspace) REFERENCES workspaces(id));
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_leases_active ON hardware_leases(node,device_id) WHERE released IS NULL;
CREATE TABLE IF NOT EXISTS environments(project TEXT NOT NULL,name TEXT NOT NULL,version INTEGER NOT NULL,base_image TEXT NOT NULL,dockerfile TEXT NOT NULL,image TEXT NOT NULL,context_name TEXT NOT NULL DEFAULT '',architecture TEXT NOT NULL DEFAULT '',node TEXT,status TEXT NOT NULL,image_id TEXT,created REAL NOT NULL,updated REAL NOT NULL,PRIMARY KEY(project,name),FOREIGN KEY(project) REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS environment_exports(artifact TEXT PRIMARY KEY,project TEXT NOT NULL,name TEXT NOT NULL,version INTEGER NOT NULL,created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS workspace_snapshots(id TEXT PRIMARY KEY,workspace TEXT NOT NULL,artifact TEXT NOT NULL,created REAL NOT NULL,FOREIGN KEY(workspace) REFERENCES workspaces(id));
CREATE TABLE IF NOT EXISTS operation_artifacts(operation TEXT NOT NULL,artifact TEXT NOT NULL,PRIMARY KEY(operation,artifact),FOREIGN KEY(operation) REFERENCES operations(id));
CREATE TABLE IF NOT EXISTS artifact_grants(workspace TEXT NOT NULL,artifact TEXT NOT NULL,PRIMARY KEY(workspace,artifact));
CREATE TABLE IF NOT EXISTS workspace_repositories(workspace TEXT NOT NULL,repository TEXT NOT NULL,revision TEXT NOT NULL,PRIMARY KEY(workspace,repository));
CREATE TABLE IF NOT EXISTS repository_metadata(project TEXT NOT NULL,full_name TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'source',default_ref TEXT NOT NULL DEFAULT '',PRIMARY KEY(project,full_name),FOREIGN KEY(project,full_name) REFERENCES repositories(project,full_name));
CREATE TABLE IF NOT EXISTS planning_boards(project TEXT PRIMARY KEY,github_id TEXT NOT NULL,url TEXT NOT NULL,title TEXT NOT NULL,updated REAL NOT NULL,FOREIGN KEY(project) REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS libraries(id TEXT PRIMARY KEY,title TEXT NOT NULL,description TEXT NOT NULL,kind TEXT NOT NULL,created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS library_sources(library TEXT NOT NULL,repository TEXT NOT NULL,revision TEXT NOT NULL,path TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL DEFAULT 'source',added REAL NOT NULL,PRIMARY KEY(library,repository,path),FOREIGN KEY(library) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS project_libraries(project TEXT NOT NULL,library TEXT NOT NULL,linked REAL NOT NULL,PRIMARY KEY(project,library),FOREIGN KEY(project) REFERENCES projects(id),FOREIGN KEY(library) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS library_objects(library TEXT NOT NULL,name TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,kind TEXT NOT NULL,created REAL NOT NULL,PRIMARY KEY(library,name),FOREIGN KEY(library) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS library_uploads(id TEXT PRIMARY KEY,library TEXT NOT NULL,name TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,kind TEXT NOT NULL,received INTEGER NOT NULL,created REAL NOT NULL,expires REAL NOT NULL,FOREIGN KEY(library) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS project_grants(project TEXT NOT NULL,principal TEXT NOT NULL,role TEXT NOT NULL,created REAL NOT NULL,PRIMARY KEY(project,principal),FOREIGN KEY(project) REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS task_claims(project TEXT NOT NULL,work_key TEXT NOT NULL,generation INTEGER NOT NULL,token_hash TEXT,status TEXT NOT NULL,expires REAL NOT NULL,results TEXT NOT NULL DEFAULT '[]',created REAL NOT NULL,updated REAL NOT NULL,PRIMARY KEY(project,work_key),FOREIGN KEY(project) REFERENCES projects(id));
CREATE INDEX IF NOT EXISTS idx_task_claims_status ON task_claims(project,status,expires);
CREATE TABLE IF NOT EXISTS revoked_tokens(digest TEXT PRIMARY KEY,expires REAL NOT NULL,created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS access_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,principal TEXT NOT NULL,project TEXT NOT NULL,action TEXT NOT NULL,allowed INTEGER NOT NULL,created REAL NOT NULL);
CREATE INDEX IF NOT EXISTS idx_access_audit_created ON access_audit(created);
CREATE TABLE IF NOT EXISTS pool_images(id TEXT PRIMARY KEY,image TEXT NOT NULL,architecture TEXT NOT NULL DEFAULT '',description TEXT NOT NULL DEFAULT '',created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS pool_software(id TEXT PRIMARY KEY,image_id TEXT NOT NULL,packages TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',created REAL NOT NULL,FOREIGN KEY(image_id) REFERENCES pool_images(id));
`

var (
	validID           = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	validRepo         = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	validImage        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_@-]{1,200}$`)
	validDeviceID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	validArtifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	validRelative     = regexp.MustCompile(`^[A-Za-z0-9_.-][A-Za-z0-9_./+@ -]+$`)
)

func validGitRepository(repo string) bool {
	if !validRepo.MatchString(repo) {
		return false
	}
	parts := strings.Split(repo, "/")
	for _, part := range parts {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }
func randomHex(n int) string {
	data := make([]byte, n)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}
func checksum(data []byte) string  { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func equalSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func asString(v any) string        { s, _ := v.(string); return s }
func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}
func asInt(v any) int     { return int(asFloat(v)) }
func asBool(v any) bool   { b, _ := v.(bool); return b }
func encode(v any) string { buf, _ := json.Marshal(v); return string(buf) }
func decodeObject(s string) map[string]any {
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}
func invalid(format string, args ...any) error { return fmt.Errorf(format, args...) }
func first(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
func requireID(id string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid identifier")
	}
	return nil
}
func requireProject(db *Database, id string) error {
	if err := requireID(id); err != nil {
		return err
	}
	rows, err := db.queryLocked("SELECT id FROM projects WHERE id=?", id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("unknown project")
	}
	return nil
}
func validRelativePath(path string) bool {
	if path == "" || len(path) > 511 || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

func (db *Database) transaction(fn func() error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.execLocked("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer db.execLocked("ROLLBACK")
	if err := fn(); err != nil {
		return err
	}
	return db.execLocked("COMMIT")
}
func (db *Database) queryOne(sql string, args ...any) (map[string]any, error) {
	r, e := db.query(sql, args...)
	return first(r), e
}
func (db *Database) lockedOne(sql string, args ...any) (map[string]any, error) {
	r, e := db.queryLocked(sql, args...)
	return first(r), e
}
func (db *Database) countLocked(sql string, args ...any) (float64, error) {
	r, e := db.lockedOne(sql, args...)
	if e != nil {
		return 0, e
	}
	if r == nil {
		return 0, nil
	}
	for _, v := range r {
		return asFloat(v), nil
	}
	return 0, nil
}

func (db *Database) InitSchema() error {
	for _, part := range strings.Split(schema, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if err := db.exec(part); err != nil {
			return fmt.Errorf("database schema: %w", err)
		}
	}
	return nil
}

// RandomCredential creates the owner credential used by the Go controller CLI.
func RandomCredential() string { return randomHex(40) }
