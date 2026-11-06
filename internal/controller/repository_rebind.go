package controller

import (
	"errors"
	"fmt"
)

// Repository names are mutable provider locators. Historical contribution and
// indexed-source provenance remains pinned to the original name and commit.
func (db *Database) repositoryRebindPreflightLocked(before, after string) error {
	if !validGitRepository(before) || !validGitRepository(after) || before == after {
		return errors.New("invalid repository name transition")
	}
	conflicts := []struct {
		query   string
		message string
	}{
		{`SELECT 1 FROM repositories a JOIN repositories b ON a.project=b.project
			WHERE a.full_name=? AND b.full_name=? LIMIT 1`, "repository already linked under destination name"},
		{`SELECT 1 FROM library_sources a JOIN library_sources b
			ON a.library=b.library AND a.path=b.path
			WHERE a.repository=? AND b.repository=? LIMIT 1`, "library already references destination repository"},
		{`SELECT 1 FROM workspace_repositories a JOIN workspace_repositories b
			ON a.workspace=b.workspace WHERE a.repository=? AND b.repository=? LIMIT 1`,
			"workspace contains both repository names"},
	}
	for _, check := range conflicts {
		row, err := db.lockedOne(check.query, before, after)
		if err != nil {
			return err
		}
		if row != nil {
			return errors.New(check.message)
		}
	}
	active, err := db.lockedOne(`SELECT 1 FROM workspace_repositories wr
		JOIN workspaces w ON w.id=wr.workspace
		WHERE wr.repository=? AND w.status IN ('starting','ready','stopping','failed')
		LIMIT 1`, before)
	if err != nil {
		return err
	}
	if active != nil {
		return errors.New("repository has an active workspace; release it before changing the provider locator")
	}
	return nil
}

// CheckRepositoryRebind permits an external provider mutation only when the
// controller can reconcile references without known key or lease conflicts.
func (db *Database) CheckRepositoryRebind(before, after string) error {
	return db.transaction(func() error {
		return db.repositoryRebindPreflightLocked(before, after)
	})
}

// RebindRepositoryReferences runs only after a provider confirms the immutable
// repository identity at the new locator. All internal references move in one
// transaction; pinned historical provenance is intentionally not rewritten.
func (db *Database) RebindRepositoryReferences(before, after string) (map[string]any, error) {
	updated := map[string]any{}
	err := db.transaction(func() error {
		if err := db.repositoryRebindPreflightLocked(before, after); err != nil {
			return err
		}
		tables := []struct {
			name  string
			query string
		}{
			{"linked_projects", "SELECT count(*) n FROM repositories WHERE full_name=?"},
			{"library_sources", "SELECT count(*) n FROM library_sources WHERE repository=?"},
			{"workspace_checkouts", "SELECT count(*) n FROM workspace_repositories WHERE repository=?"},
			{"knowledge_destinations", "SELECT count(*) n FROM project_knowledge_policies WHERE repository=?"},
		}
		for _, table := range tables {
			row, err := db.lockedOne(table.query, before)
			if err != nil {
				return err
			}
			if row == nil {
				return fmt.Errorf("unable to inspect repository references in %s", table.name)
			}
			updated[table.name] = asInt(row["n"])
		}
		// The new parent row must exist before metadata is updated, because
		// SQLite enforces the composite repository foreign key immediately.
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO repositories(project,full_name)
				SELECT project,? FROM repositories WHERE full_name=?`, []any{after, before}},
			{"UPDATE repository_metadata SET full_name=? WHERE full_name=?", []any{after, before}},
			{"DELETE FROM repositories WHERE full_name=?", []any{before}},
			{"UPDATE library_sources SET repository=? WHERE repository=?", []any{after, before}},
			{"UPDATE workspace_repositories SET repository=? WHERE repository=?", []any{after, before}},
			{"UPDATE project_knowledge_policies SET repository=?,updated=? WHERE repository=?",
				[]any{after, stamp(), before}},
		}
		for _, statement := range statements {
			if err := db.execLocked(statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	updated["previous_repository"] = before
	updated["repository"] = after
	updated["reconciled"] = true
	return updated, nil
}
