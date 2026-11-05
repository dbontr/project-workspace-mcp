package controller

import "errors"

// RevokeNode invalidates a worker's bearer credential without discarding
// pending operations, workspace history, or hardware-lease evidence.
// Re-enrollment of the same name requires a separate administrator action.
func (db *Database) RevokeNode(name string) (map[string]any, error) {
	if !validID.MatchString(name) {
		return nil, errors.New("invalid worker ID")
	}
	var result map[string]any
	err := db.transaction(func() error {
		node, err := db.lockedOne("SELECT id FROM nodes WHERE id=?", name)
		if err != nil {
			return err
		}
		if node == nil {
			return errors.New("worker not found")
		}
		workspaces, err := db.countLocked(`SELECT count(*) n FROM workspaces
			WHERE node=? AND status IN ('starting','ready','stopping','failed')`, name)
		if err != nil {
			return err
		}
		hardware, err := db.countLocked(`SELECT count(*) n FROM hardware_leases
			WHERE node=? AND released IS NULL`, name)
		if err != nil {
			return err
		}
		operations, err := db.countLocked(`SELECT count(*) n FROM operations
			WHERE node=? AND status IN ('queued','leased')`, name)
		if err != nil {
			return err
		}
		// Never expose replacement credentials through a revocation response.
		rotated := hashToken(randomHex(36))
		if err := db.execLocked("UPDATE nodes SET token_hash=?,last_seen=0 WHERE id=?",
			rotated, name); err != nil {
			return err
		}
		if err := db.execLocked("UPDATE personal_task_worker_access SET allowed=0,updated=? WHERE node=?", stamp(), name); err != nil {
			return err
		}
		result = map[string]any{"worker_id": name, "revoked": true,
			"active_workspaces": workspaces, "active_hardware_leases": hardware,
			"unfinished_operations": operations,
		}
		return nil
	})
	return result, err
}
