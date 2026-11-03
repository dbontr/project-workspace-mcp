package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (db *Database) objectDir() string { return filepath.Join(db.artifactDir(), "objects") }
func (db *Database) objectPath(digest string) string {
	return filepath.Join(db.objectDir(), digest[:2], digest)
}

func (db *Database) LibraryObjects(id string) ([]map[string]any, error) {
	if _, e := db.Library(id); e != nil {
		return nil, e
	}
	return db.query(`SELECT library,name,sha256,size,kind,created FROM library_objects WHERE library=? ORDER BY name`, id)
}
func (db *Database) LibraryObject(id, name string) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	if !validArtifactName.MatchString(name) {
		return nil, errors.New("invalid library object name")
	}
	row, e := db.queryOne(`SELECT library,name,sha256,size,kind,created FROM library_objects WHERE library=? AND name=?`, id, name)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("library object not found")
	}
	return row, nil
}

// StoreLibraryObject stores binary data once by SHA-256, regardless of how many
// libraries reference the same bytes. Names are immutable to avoid lost history.
func (db *Database) StoreLibraryObject(id, name, kind, expected string, body io.Reader, maxBytes int64) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	if !validArtifactName.MatchString(name) || !objectKinds[kind] || !shaPattern.MatchString(expected) {
		return nil, errors.New("invalid library object metadata")
	}
	if maxBytes <= 0 || maxBytes > 100<<30 {
		return nil, errors.New("invalid object size limit")
	}
	if body == nil {
		return nil, errors.New("missing object body")
	}
	if _, e := db.Library(id); e != nil {
		return nil, e
	}
	parent := filepath.Join(db.objectDir(), expected[:2])
	if e := os.MkdirAll(parent, 0700); e != nil {
		return nil, e
	}
	tempFile, e := os.CreateTemp(parent, ".incoming-")
	if e != nil {
		return nil, e
	}
	temp := tempFile.Name()
	defer os.Remove(temp)
	hash := sha256.New()
	size, e := io.Copy(io.MultiWriter(tempFile, hash), io.LimitReader(body, maxBytes+1))
	if e != nil {
		tempFile.Close()
		return nil, e
	}
	if size > maxBytes {
		tempFile.Close()
		return nil, errors.New("library object exceeds configured size limit")
	}
	if e := tempFile.Sync(); e != nil {
		tempFile.Close()
		return nil, e
	}
	if e := tempFile.Close(); e != nil {
		return nil, e
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return nil, errors.New("library object SHA-256 mismatch")
	}
	target := db.objectPath(expected)
	linked := false
	err := db.transaction(func() error {
		row, e := db.lockedOne(`SELECT 1 AS present FROM libraries WHERE id=?`, id)
		if e != nil {
			return e
		}
		if row == nil {
			return errors.New("library not found")
		}
		exists, e := db.lockedOne(`SELECT 1 AS present FROM library_objects WHERE library=? AND name=?`, id, name)
		if e != nil {
			return e
		}
		if exists != nil {
			return errors.New("library object name already exists")
		}
		if e := os.Link(temp, target); e != nil {
			if !os.IsExist(e) {
				return e
			}
			info, e := os.Lstat(target)
			if e != nil || !info.Mode().IsRegular() || info.Size() != size {
				return errors.New("content-addressed object conflict")
			}
		} else {
			linked = true
		}
		return db.execLocked(`INSERT INTO library_objects(library,name,sha256,size,kind,created) VALUES(?,?,?,?,?,?)`, id, name, expected, size, kind, stamp())
	})
	if err != nil {
		if linked {
			_ = os.Remove(target)
		}
		return nil, err
	}
	return map[string]any{"library": id, "name": name, "sha256": expected, "size": size, "kind": kind}, nil
}
func (db *Database) OpenLibraryObject(id, name string) (*os.File, map[string]any, error) {
	obj, e := db.LibraryObject(id, name)
	if e != nil {
		return nil, nil, e
	}
	digest := asString(obj["sha256"])
	if !shaPattern.MatchString(digest) {
		return nil, nil, errors.New("corrupt object digest")
	}
	path := db.objectPath(digest)
	file, e := os.Open(path)
	if e != nil {
		return nil, nil, e
	}
	stat, e := file.Stat()
	if e != nil || !stat.Mode().IsRegular() || stat.Size() != int64(asFloat(obj["size"])) {
		file.Close()
		return nil, nil, fmt.Errorf("stored object size or type mismatch")
	}
	return file, obj, nil
}
