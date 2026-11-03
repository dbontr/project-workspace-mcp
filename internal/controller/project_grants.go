package controller

import (
	"errors"
	"strings"
)

func grantLevel(role string) int {
	switch role {
	case "viewer":
		return 1
	case "editor":
		return 2
	case "owner":
		return 3
	default:
		return 0
	}
}

func validPrincipal(subject string) bool {
	return strings.HasPrefix(subject, "oidc:") && len(subject) <= 1024 &&
		len(subject) > len("oidc:") && !strings.ContainsAny(subject, "\x00\r\n")
}

func (db *Database) ProjectRole(project, principal string) (string, error) {
	if err := requireID(project); err != nil {
		return "", err
	}
	row, err := db.queryOne("SELECT role FROM project_grants WHERE project=? AND principal=?", project, principal)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", nil
	}
	return asString(row["role"]), nil
}

// SetProjectGrant uses the previous role as a revision guard.
// The empty expected role means that no grant may already exist.
func (db *Database) SetProjectGrant(project, principal, role, expected string) (map[string]any, error) {
	if !validPrincipal(principal) || (role != "revoke" && grantLevel(role) == 0) ||
		(expected != "" && grantLevel(expected) == 0) {
		return nil, errors.New("invalid project grant")
	}
	var result map[string]any
	err := db.transaction(func() error {
		if err := requireProject(db, project); err != nil {
			return err
		}
		row, err := db.lockedOne("SELECT role FROM project_grants WHERE project=? AND principal=?", project, principal)
		if err != nil {
			return err
		}
		actual := ""
		if row != nil {
			actual = asString(row["role"])
		}
		if actual != expected {
			return errors.New("project grant revision conflict")
		}
		if role == "revoke" {
			if err := db.execLocked("DELETE FROM project_grants WHERE project=? AND principal=?", project, principal); err != nil {
				return err
			}
		} else if err := db.execLocked("INSERT INTO project_grants(project,principal,role,created) VALUES(?,?,?,?) ON CONFLICT(project,principal) DO UPDATE SET role=excluded.role",
			project, principal, role, stamp()); err != nil {
			return err
		}
		result = map[string]any{"project": project, "principal": principal, "role": role, "previous_role": actual}
		return nil
	})
	return result, err
}

func (db *Database) ProjectGrants(project string) ([]map[string]any, error) {
	if err := requireID(project); err != nil {
		return nil, err
	}
	return db.query("SELECT principal,role,created FROM project_grants WHERE project=? ORDER BY principal", project)
}

func (db *Database) ProjectsForPrincipal(principal string) ([]map[string]any, error) {
	return db.query("SELECT p.id,p.title,p.created,g.role FROM projects p JOIN project_grants g ON g.project=p.id WHERE g.principal=? ORDER BY p.id", principal)
}

func (db *Database) AuditAccess(principal, project, action string, allowed bool) {
	if len(principal) > 1024 || len(project) > 63 || len(action) > 128 {
		return
	}
	flag := 0
	if allowed {
		flag = 1
	}
	_ = db.exec("INSERT INTO access_audit(principal,project,action,allowed,created) VALUES(?,?,?,?,?)",
		principal, project, action, flag, stamp())
}

func (db *Database) AccessAudit(limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid audit limit")
	}
	return db.query("SELECT id,principal,project,action,allowed,created FROM access_audit ORDER BY id DESC LIMIT ?", limit)
}
