package controller

import "errors"

// Standalone jobs may run only on workers approved through an administrator
// action. Worker heartbeats cannot grant their own task execution authority.
func (db *Database) AuthorizePersonalTaskWorker(node string, allowed, confirm bool) (map[string]any, error) {
	if !confirm || !validID.MatchString(node) {
		return nil, errors.New("explicit worker identity and approval confirmation required")
	}
	err := db.transaction(func() error {
		if row, err := db.lockedOne("SELECT id FROM nodes WHERE id=?", node); err != nil {
			return err
		} else if row == nil {
			return errors.New("worker not found")
		}
		enabled := 0
		if allowed {
			enabled = 1
		}
		return db.execLocked(`INSERT INTO personal_task_worker_access(node,allowed,updated)
			VALUES(?,?,?) ON CONFLICT(node) DO UPDATE SET
			allowed=excluded.allowed,updated=excluded.updated`, node, enabled, stamp())
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"worker_id": node, "standalone_task_execution": allowed}, nil
}

func (db *Database) PersonalTaskWorkerAccess() ([]map[string]any, error) {
	return db.query(`SELECT n.id AS node,coalesce(a.allowed,0) AS allowed,n.last_seen
		FROM nodes n LEFT JOIN personal_task_worker_access a ON a.node=n.id
		ORDER BY n.id`)
}

func (db *Database) personalWorkerAllowedLocked(node string) (bool, error) {
	row, err := db.lockedOne("SELECT allowed FROM personal_task_worker_access WHERE node=?", node)
	if err != nil {
		return false, err
	}
	return row != nil && asInt(row["allowed"]) == 1, nil
}
