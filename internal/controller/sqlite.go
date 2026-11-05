package controller

/*
#cgo pkg-config: sqlite3
#include <sqlite3.h>
#include <stdlib.h>
static int pwm_bind_text(sqlite3_stmt *stmt, int position, const char *text) {
    return sqlite3_bind_text(stmt, position, text, -1, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Database stores project, artifact, and scheduler state in one SQLite file.
type Database struct {
	mu       sync.Mutex
	handle   *C.sqlite3
	filename string
}

func OpenDatabase(filename string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	cpath := C.CString(filename)
	defer C.free(unsafe.Pointer(cpath))
	var handle *C.sqlite3
	rc := C.sqlite3_open_v2(cpath, &handle, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX, nil)
	if rc != C.SQLITE_OK {
		message := "unable to open database"
		if handle != nil {
			message = C.GoString(C.sqlite3_errmsg(handle))
			C.sqlite3_close(handle)
		}
		return nil, errors.New(message)
	}
	db := &Database{handle: handle, filename: filename}
	C.sqlite3_busy_timeout(handle, 30000)
	if err := db.exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.InitSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (db *Database) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.handle == nil {
		return nil
	}
	rc := C.sqlite3_close(db.handle)
	if rc != C.SQLITE_OK {
		return fmt.Errorf("SQLite close failed: %d", int(rc))
	}
	db.handle = nil
	return nil
}

func (db *Database) prepare(statement string, args ...any) (*C.sqlite3_stmt, error) {
	query := C.CString(statement)
	defer C.free(unsafe.Pointer(query))
	var stmt *C.sqlite3_stmt
	if C.sqlite3_prepare_v2(db.handle, query, -1, &stmt, nil) != C.SQLITE_OK {
		return nil, errors.New(C.GoString(C.sqlite3_errmsg(db.handle)))
	}
	for n, arg := range args {
		index := C.int(n + 1)
		var rc C.int
		switch v := arg.(type) {
		case string:
			value := C.CString(v)
			rc = C.pwm_bind_text(stmt, index, value)
			C.free(unsafe.Pointer(value))
		case int:
			rc = C.sqlite3_bind_int64(stmt, index, C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(stmt, index, C.sqlite3_int64(v))
		case float64:
			rc = C.sqlite3_bind_double(stmt, index, C.double(v))
		case nil:
			rc = C.sqlite3_bind_null(stmt, index)
		default:
			C.sqlite3_finalize(stmt)
			return nil, fmt.Errorf("unsupported SQL argument type %T", arg)
		}
		if rc != C.SQLITE_OK {
			C.sqlite3_finalize(stmt)
			return nil, errors.New(C.GoString(C.sqlite3_errmsg(db.handle)))
		}
	}
	return stmt, nil
}

// execLocked must be called with db.mu held. One connection serializes writes.
func (db *Database) execLocked(statement string, args ...any) error {
	stmt, err := db.prepare(statement, args...)
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(stmt)
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return nil
		}
		if rc != C.SQLITE_ROW {
			return errors.New(C.GoString(C.sqlite3_errmsg(db.handle)))
		}
	}
}
func (db *Database) exec(statement string, args ...any) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.execLocked(statement, args...)
}
func (db *Database) queryLocked(statement string, args ...any) ([]map[string]any, error) {
	stmt, err := db.prepare(statement, args...)
	if err != nil {
		return nil, err
	}
	defer C.sqlite3_finalize(stmt)
	rows := make([]map[string]any, 0)
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			break
		}
		if rc != C.SQLITE_ROW {
			return nil, errors.New(C.GoString(C.sqlite3_errmsg(db.handle)))
		}
		row := make(map[string]any)
		for i := C.int(0); i < C.sqlite3_column_count(stmt); i++ {
			name := C.GoString(C.sqlite3_column_name(stmt, i))
			switch C.sqlite3_column_type(stmt, i) {
			case C.SQLITE_INTEGER:
				row[name] = int64(C.sqlite3_column_int64(stmt, i))
			case C.SQLITE_FLOAT:
				row[name] = float64(C.sqlite3_column_double(stmt, i))
			case C.SQLITE_TEXT:
				row[name] = C.GoString((*C.char)(unsafe.Pointer(C.sqlite3_column_text(stmt, i))))
			case C.SQLITE_NULL:
				row[name] = nil
			default:
				return nil, fmt.Errorf("unsupported SQLite column type in %s", name)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func (db *Database) query(statement string, args ...any) ([]map[string]any, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.queryLocked(statement, args...)
}

func (db *Database) CreateProject(id, title string) (map[string]any, error) {
	if !validID.MatchString(id) || id == personalComputeScope || len(strings.TrimSpace(title)) == 0 || len(title) > 256 {
		return nil, errors.New("invalid project ID or title")
	}
	err := db.exec("INSERT INTO projects VALUES(?,?,?)", id, title, float64(time.Now().UnixNano())/1e9)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "title": title}, nil
}
func (db *Database) Projects() ([]map[string]any, error) {
	return db.query("SELECT id,title,created FROM projects ORDER BY id")
}
func (db *Database) ReadNote(project, name string) (map[string]any, error) {
	rows, err := db.query("SELECT project,name,body,version,updated FROM notes WHERE project=? AND name=?", project, name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("note not found")
	}
	return rows[0], nil
}
func (db *Database) WriteNote(project, name, body string, version int) (map[string]any, error) {
	if !validID.MatchString(project) || !validID.MatchString(name) || len(body) > 512000 || version < 0 {
		return nil, errors.New("invalid project note")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.execLocked("BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer db.execLocked("ROLLBACK")
	rows, err := db.queryLocked("SELECT version FROM notes WHERE project=? AND name=?", project, name)
	if err != nil {
		return nil, err
	}
	actual := int64(0)
	if len(rows) > 0 {
		actual = rows[0]["version"].(int64)
	}
	if actual != int64(version) {
		return nil, fmt.Errorf("version conflict: expected %d, actual %d", version, actual)
	}
	exists, err := db.queryLocked("SELECT 1 as present FROM projects WHERE id=?", project)
	if err != nil {
		return nil, err
	}
	if len(exists) == 0 {
		return nil, errors.New("unknown project")
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if err := db.execLocked(`INSERT INTO notes VALUES(?,?,?,?,?) ON CONFLICT(project,name) DO UPDATE SET body=excluded.body,version=excluded.version,updated=excluded.updated`, project, name, body, version+1, now); err != nil {
		return nil, err
	}
	if err := db.execLocked("COMMIT"); err != nil {
		return nil, err
	}
	return map[string]any{"project": project, "name": name, "version": version + 1}, nil
}
func (db *Database) SearchNotes(project, query string) ([]map[string]any, error) {
	if !validID.MatchString(project) || len(query) > 2048 {
		return nil, errors.New("invalid search")
	}
	return db.query("SELECT project,name,version,substr(body,1,4000) AS excerpt FROM notes WHERE project=? AND (name LIKE ? OR body LIKE ?) LIMIT 30", project, "%"+query+"%", "%"+query+"%")
}
