package controller

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var validCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

var repositoryRoles = map[string]bool{
	"source": true, "research": true, "lean": true, "hardware": true, "documentation": true, "planning": true,
}

// RepositoryMemberships exposes a project's repositories without duplicating Git history.
func (db *Database) RepositoryMemberships(project string) ([]map[string]any, error) {
	if e := requireID(project); e != nil {
		return nil, e
	}
	return db.query(`SELECT r.full_name AS repository,COALESCE(m.role,'source') AS role,
        COALESCE(m.default_ref,'') AS default_ref FROM repositories r
        LEFT JOIN repository_metadata m ON m.project=r.project AND m.full_name=r.full_name
        WHERE r.project=? ORDER BY r.full_name`, project)
}

func (db *Database) LinkRepositoryWithRole(project, repo, role, ref string) (map[string]any, error) {
	if !validSourceRepository(repo) {
		return nil, errors.New("invalid repository")
	}
	if role == "" {
		role = "source"
	}
	if !repositoryRoles[role] {
		return nil, errors.New("invalid repository role")
	}
	if len(ref) > 200 || (ref != "" && !regexRef(ref)) {
		return nil, errors.New("invalid repository ref")
	}
	e := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		if e := db.execLocked(`INSERT OR IGNORE INTO repositories(project,full_name) VALUES(?,?)`, project, repo); e != nil {
			return e
		}
		return db.execLocked(`INSERT INTO repository_metadata(project,full_name,role,default_ref) VALUES(?,?,?,?)
            ON CONFLICT(project,full_name) DO UPDATE SET role=excluded.role,default_ref=excluded.default_ref`, project, repo, role, ref)
	})
	return map[string]any{"project": project, "repository": repo, "role": role, "default_ref": ref}, e
}

var libraryKinds = map[string]bool{"lean": true, "research": true, "code": true, "mixed": true}
var sourceKinds = map[string]bool{"source": true, "research": true, "formalization": true, "documentation": true}
var objectKinds = map[string]bool{"build-cache": true, "dataset": true, "paper": true, "result": true, "archive": true, "study-pack": true}

func (db *Database) CreateLibrary(id, title, description, kind string) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	if title == "" || len(title) > 200 || len(description) > 8000 || !libraryKinds[kind] {
		return nil, errors.New("invalid library metadata")
	}
	e := db.exec(`INSERT INTO libraries(id,title,description,kind,created) VALUES(?,?,?,?,?)`, id, title, description, kind, stamp())
	if e != nil {
		return nil, fmt.Errorf("library already exists or invalid: %w", e)
	}
	return map[string]any{"id": id, "title": title, "description": description, "kind": kind}, nil
}
func (db *Database) Libraries() ([]map[string]any, error) {
	return db.query(`SELECT id,title,description,kind,created FROM libraries ORDER BY id`)
}
func (db *Database) Library(id string) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	row, e := db.queryOne(`SELECT id,title,description,kind,created FROM libraries WHERE id=?`, id)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("library not found")
	}
	return row, nil
}
func (db *Database) LinkLibrary(project, id string) (map[string]any, error) {
	e := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		row, e := db.lockedOne(`SELECT id FROM libraries WHERE id=?`, id)
		if e != nil {
			return e
		}
		if row == nil {
			return errors.New("library not found")
		}
		return db.execLocked(`INSERT OR IGNORE INTO project_libraries(project,library,linked) VALUES(?,?,?)`, project, id, stamp())
	})
	return map[string]any{"project": project, "library": id}, e
}
func (db *Database) ProjectLibraries(project string) ([]map[string]any, error) {
	if e := requireID(project); e != nil {
		return nil, e
	}
	return db.query(`SELECT l.id,l.title,l.description,l.kind,p.linked FROM project_libraries p
        JOIN libraries l ON l.id=p.library WHERE p.project=? ORDER BY l.id`, project)
}
func (db *Database) libraryAttached(project, id string) bool {
	if requireID(project) != nil || requireID(id) != nil {
		return false
	}
	result, e := db.query(`SELECT 1 AS present FROM project_libraries WHERE project=? AND library=?`, project, id)
	return e == nil && len(result) > 0
}
func (db *Database) AddLibrarySource(id, repo, revision, path, kind string) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	if !validSourceRepository(repo) || !validCommitSHA.MatchString(revision) {
		return nil, errors.New("library sources require a repository and immutable commit SHA")
	}
	if path != "" && (!validRelativePath(path) || strings.Contains(path, "\\")) {
		return nil, errors.New("invalid library source path")
	}
	if kind == "" {
		kind = "source"
	}
	if !sourceKinds[kind] {
		return nil, errors.New("invalid library source kind")
	}
	e := db.transaction(func() error {
		row, e := db.lockedOne(`SELECT 1 AS present FROM libraries WHERE id=?`, id)
		if e != nil {
			return e
		}
		if row == nil {
			return errors.New("library not found")
		}
		return db.execLocked(`INSERT INTO library_sources(library,repository,revision,path,kind,added)
            VALUES(?,?,?,?,?,?) ON CONFLICT(library,repository,path)
            DO UPDATE SET revision=excluded.revision,kind=excluded.kind,added=excluded.added`, id, repo, revision, path, kind, stamp())
	})
	return map[string]any{"library": id, "repository": repo, "revision": revision, "path": path, "kind": kind}, e
}
func (db *Database) LibrarySources(id string) ([]map[string]any, error) {
	if _, e := db.Library(id); e != nil {
		return nil, e
	}
	return db.query(`SELECT repository,revision,path,kind,added FROM library_sources WHERE library=? ORDER BY repository,path`, id)
}
func (db *Database) LibrarySource(id, repo, path string) (map[string]any, error) {
	if e := requireID(id); e != nil {
		return nil, e
	}
	row, e := db.queryOne(`SELECT repository,revision,path,kind FROM library_sources WHERE library=? AND repository=? AND path=?`, id, repo, path)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("library source not found")
	}
	return row, nil
}

// Resolution returns references rather than copying shared source into every project.
func (db *Database) ResolveLibrary(project, id string) (map[string]any, error) {
	if !db.libraryAttached(project, id) {
		return nil, errors.New("library not linked to project")
	}
	info, e := db.Library(id)
	if e != nil {
		return nil, e
	}
	sources, e := db.LibrarySources(id)
	if e != nil {
		return nil, e
	}
	objects, e := db.LibraryObjects(id)
	if e != nil {
		return nil, e
	}
	return map[string]any{"library": info, "sources": sources, "objects": objects}, nil
}

func libraryFileAllowed(file, sourcePath string) bool {
	if !validFilePath(file) {
		return false
	}
	if sourcePath == "" {
		return true
	}
	return file == sourcePath || strings.HasPrefix(file, sourcePath+"/")
}
