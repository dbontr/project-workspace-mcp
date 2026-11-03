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

const libraryChunkSize = int64(8 << 20)
const libraryUploadTTL = 24 * 60 * 60

func (db *Database) uploadDir() string           { return filepath.Join(db.artifactDir(), "staging") }
func (db *Database) uploadPath(id string) string { return filepath.Join(db.uploadDir(), id) }

func (db *Database) ExpireLibraryUploads() error {
	return db.transaction(func() error {
		rows, e := db.queryLocked(`SELECT id FROM library_uploads WHERE expires<?`, stamp())
		if e != nil {
			return e
		}
		for _, row := range rows {
			if e := db.execLocked(`DELETE FROM library_uploads WHERE id=?`, row["id"]); e != nil {
				return e
			}
			_ = os.Remove(db.uploadPath(asString(row["id"])))
		}
		return nil
	})
}
func (db *Database) StartLibraryUpload(library, name, kind, sha string, size, maxBytes int64) (map[string]any, error) {
	if e := requireID(library); e != nil {
		return nil, e
	}
	if !validArtifactName.MatchString(name) || !objectKinds[kind] || !shaPattern.MatchString(sha) || size <= 0 || size > maxBytes || maxBytes > 100<<30 {
		return nil, errors.New("invalid library upload metadata or size")
	}
	if _, e := db.Library(library); e != nil {
		return nil, e
	}
	if e := db.ExpireLibraryUploads(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(db.uploadDir(), 0700); e != nil {
		return nil, e
	}
	id := randomHex(16)
	var result map[string]any
	path := db.uploadPath(id)
	e := db.transaction(func() error {
		existing, e := db.lockedOne(`SELECT 1 AS present FROM library_objects WHERE library=? AND name=?`, library, name)
		if e != nil {
			return e
		}
		if existing != nil {
			return errors.New("library object name already exists")
		}
		file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if e := file.Close(); e != nil {
			_ = os.Remove(path)
			return e
		}
		created := stamp()
		if e := db.execLocked(`INSERT INTO library_uploads(id,library,name,sha256,size,kind,received,created,expires) VALUES(?,?,?,?,?,?,0,?,?)`, id, library, name, sha, size, kind, created, created+libraryUploadTTL); e != nil {
			_ = os.Remove(path)
			return e
		}
		result = map[string]any{"upload_id": id, "library": library, "name": name, "sha256": sha, "size": size, "received_bytes": int64(0), "chunk_size": libraryChunkSize, "expires": created + libraryUploadTTL}
		return nil
	})
	return result, e
}
func (db *Database) uploadRow(library, id string) (map[string]any, error) {
	if e := requireID(library); e != nil {
		return nil, e
	}
	if !validArtifactName.MatchString(id) || len(id) != 32 {
		return nil, errors.New("invalid upload ID")
	}
	row, e := db.queryOne(`SELECT id,library,name,sha256,size,kind,received,expires FROM library_uploads WHERE library=? AND id=?`, library, id)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("library upload not found")
	}
	if asFloat(row["expires"]) < stamp() {
		return nil, errors.New("library upload expired")
	}
	row["chunk_size"] = libraryChunkSize
	return row, nil
}
func (db *Database) AppendLibraryChunk(library, id string, offset int64, reader io.Reader) (map[string]any, error) {
	if reader == nil || offset < 0 {
		return nil, errors.New("invalid upload chunk")
	}
	data, e := io.ReadAll(io.LimitReader(reader, libraryChunkSize+1))
	if e != nil {
		return nil, e
	}
	if len(data) == 0 || int64(len(data)) > libraryChunkSize {
		return nil, errors.New("upload chunk must be between 1 and 8 MiB")
	}
	if _, e := db.uploadRow(library, id); e != nil {
		return nil, e
	}
	var result map[string]any
	e = db.transaction(func() error {
		current, e := db.lockedOne(`SELECT received,size,expires FROM library_uploads WHERE library=? AND id=?`, library, id)
		if e != nil {
			return e
		}
		if current == nil {
			return errors.New("library upload not found")
		}
		received := int64(asFloat(current["received"]))
		size := int64(asFloat(current["size"]))
		if asFloat(current["expires"]) < stamp() {
			return errors.New("library upload expired")
		}
		if offset != received {
			return fmt.Errorf("upload offset conflict: expected %d", received)
		}
		if received+int64(len(data)) > size {
			return errors.New("chunk exceeds declared size")
		}
		file, e := os.OpenFile(db.uploadPath(id), os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		info, e := file.Stat()
		if e != nil {
			file.Close()
			return e
		}
		if info.Size() < received {
			file.Close()
			return errors.New("upload staging file truncated")
		}
		// The database offset is authoritative after an interrupted transaction.
		if e := file.Truncate(received); e != nil {
			file.Close()
			return e
		}
		if _, e := file.Seek(received, io.SeekStart); e != nil {
			file.Close()
			return e
		}
		if _, e := file.Write(data); e != nil {
			file.Close()
			return e
		}
		if e := file.Sync(); e != nil {
			file.Close()
			return e
		}
		if e := file.Close(); e != nil {
			return e
		}
		next := received + int64(len(data))
		if e := db.execLocked(`UPDATE library_uploads SET received=? WHERE id=?`, next, id); e != nil {
			return e
		}
		result = map[string]any{"upload_id": id, "received_bytes": next, "size": size, "complete": next == size}
		return nil
	})
	return result, e
}
func (db *Database) CompleteLibraryUpload(library, id string) (map[string]any, error) {
	row, e := db.uploadRow(library, id)
	if e != nil {
		return nil, e
	}
	size := int64(asFloat(row["size"]))
	if int64(asFloat(row["received"])) != size {
		return nil, errors.New("upload is incomplete")
	}
	file, e := os.Open(db.uploadPath(id))
	if e != nil {
		return nil, e
	}
	hash := sha256.New()
	copied, e := io.Copy(hash, file)
	file.Close()
	if e != nil {
		return nil, e
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	if copied != size || sha != row["sha256"] {
		return nil, errors.New("upload checksum mismatch")
	}
	target := db.objectPath(sha)
	if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return nil, e
	}
	linked := false
	e = db.transaction(func() error {
		current, e := db.lockedOne(`SELECT received,size,sha256,name,kind,expires FROM library_uploads WHERE library=? AND id=?`, library, id)
		if e != nil {
			return e
		}
		if current == nil {
			return errors.New("library upload not found")
		}
		if asFloat(current["expires"]) < stamp() || int64(asFloat(current["received"])) != size || current["sha256"] != sha {
			return errors.New("library upload changed")
		}
		existing, e := db.lockedOne(`SELECT 1 AS present FROM library_objects WHERE library=? AND name=?`, library, row["name"])
		if e != nil {
			return e
		}
		if existing != nil {
			return errors.New("library object name already exists")
		}
		if e := os.Link(db.uploadPath(id), target); e != nil {
			if !os.IsExist(e) {
				return e
			}
			info, e := os.Stat(target)
			if e != nil || !info.Mode().IsRegular() || info.Size() != size {
				return errors.New("content-addressed object conflict")
			}
		} else {
			linked = true
		}
		if e := db.execLocked(`INSERT INTO library_objects(library,name,sha256,size,kind,created) VALUES(?,?,?,?,?,?)`, library, row["name"], sha, size, row["kind"], stamp()); e != nil {
			return e
		}
		return db.execLocked(`DELETE FROM library_uploads WHERE id=?`, id)
	})
	if e != nil {
		if linked {
			_ = os.Remove(target)
		}
		return nil, e
	}
	_ = os.Remove(db.uploadPath(id))
	return map[string]any{"library": library, "name": row["name"], "sha256": sha, "size": size, "kind": row["kind"]}, nil
}
func (db *Database) CancelLibraryUpload(library, id string) error {
	if _, e := db.uploadRow(library, id); e != nil {
		return e
	}
	e := db.exec(`DELETE FROM library_uploads WHERE library=? AND id=?`, library, id)
	if e != nil {
		return e
	}
	return os.Remove(db.uploadPath(id))
}
