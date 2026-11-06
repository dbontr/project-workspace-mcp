package controller

import (
	"errors"
	"strings"
)

func (db *Database) KnowledgePolicy(project string) (map[string]any, error) {
	if !validID.MatchString(project) {
		return nil, errors.New("invalid knowledge project")
	}
	row, err := db.queryOne("SELECT mode,repository,branch,prefix,version,updated FROM project_knowledge_policies WHERE project=?", project)
	if err != nil {
		return nil, err
	}
	if row == nil {
		check, err := db.queryOne("SELECT id FROM projects WHERE id=?", project)
		if err != nil {
			return nil, err
		}
		if check == nil {
			return nil, errors.New("project not found")
		}
		return map[string]any{"project": project, "mode": "none",
			"repository": "", "branch": "", "prefix": "", "version": 0}, nil
	}
	row["project"] = project
	return row, nil
}

func (db *Database) ConfigureKnowledgePolicy(project, mode, repo, branch, prefix string, expected int) (map[string]any, error) {
	if !validID.MatchString(project) || expected < 0 || (mode != "none" && mode != "git") {
		return nil, errors.New("invalid project knowledge policy")
	}
	if mode == "none" {
		if repo != "" || branch != "" || prefix != "" {
			return nil, errors.New("disabled knowledge cannot have a destination")
		}
	} else {
		if !validKnowledgeRepository(repo) || !validateBranch(branch) ||
			!validRelativePath(prefix) || strings.HasPrefix(prefix, ".") ||
			strings.Contains(prefix, ".git") || len(prefix) > 128 {
			return nil, errors.New("invalid Git knowledge destination")
		}
	}
	var out map[string]any
	err := db.transaction(func() error {
		if err := requireProject(db, project); err != nil {
			return err
		}
		row, err := db.lockedOne("SELECT version FROM project_knowledge_policies WHERE project=?", project)
		if err != nil {
			return err
		}
		current := 0
		if row != nil {
			current = asInt(row["version"])
		}
		if current != expected {
			return errors.New("knowledge policy revision conflict")
		}
		now := stamp()
		if err := db.execLocked(`INSERT INTO project_knowledge_policies
			(project,mode,repository,branch,prefix,version,updated)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(project) DO UPDATE SET
			mode=excluded.mode,repository=excluded.repository,
			branch=excluded.branch,prefix=excluded.prefix,
			version=excluded.version,updated=excluded.updated`,
			project, mode, repo, branch, prefix, current+1, now); err != nil {
			return err
		}
		out = map[string]any{"project": project, "mode": mode,
			"repository": repo, "branch": branch, "prefix": prefix,
			"version": current + 1, "updated": now}
		return nil
	})
	return out, err
}
