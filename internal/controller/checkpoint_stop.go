package controller

import "errors"

// CheckpointStopWorkspace records a durable snapshot request. Worker teardown
// is queued only after the exact snapshot operation has produced a verified
// project artifact. All state transitions run within SQLite transactions.
func (db *Database) CheckpointStopWorkspace(project, workspace string) (map[string]any, error) {
	var result map[string]any
	err := db.transaction(func() error {
		if !validID.MatchString(project) || !contributionArtifactID.MatchString(workspace) {
			return errors.New("invalid checkpoint workspace")
		}
		existing, err := db.lockedOne(`SELECT snapshot_operation,snapshot_id,stop_operation,status
			FROM checkpointed_stops WHERE workspace=?`, workspace)
		if err != nil {
			return err
		}
		if existing != nil && asString(existing["status"]) != "failed" {
			return db.checkpointStopResultLocked(project, workspace, &result)
		}
		ws, err := db.readyWorkspaceLocked(project, workspace)
		if err != nil {
			return err
		}
		if asFloat(ws["expires"])-stamp() < 120 {
			return errors.New("workspace lease too close to expiry for checkpoint-before-stop")
		}
		pending, err := db.countLocked(`SELECT count(*) n FROM operations
			WHERE workspace=? AND status IN ('queued','leased')`, workspace)
		if err != nil {
			return err
		}
		if pending >= 32 {
			return errors.New("workspace operation queue full")
		}
		operation, err := db.EnqueueLocked(workspace, asString(ws["node"]), "snapshot", map[string]any{})
		if err != nil {
			return err
		}
		now := stamp()
		if err := db.execLocked(`INSERT INTO checkpointed_stops
			(workspace,snapshot_operation,snapshot_id,stop_operation,status,created,updated)
			VALUES(?,?,'','','pending',?,?)
			ON CONFLICT(workspace) DO UPDATE SET
			snapshot_operation=excluded.snapshot_operation,snapshot_id='',stop_operation='',
			status='pending',updated=excluded.updated`, workspace, operation, now, now); err != nil {
			return err
		}
		result = map[string]any{"workspace_id": workspace, "status": "pending",
			"snapshot_operation": operation, "snapshot_id": "", "stop_operation": ""}
		return nil
	})
	return result, err
}

func (db *Database) checkpointStopResultLocked(project, workspace string, dest *map[string]any) error {
	if err := requireProject(db, project); err != nil {
		return err
	}
	record, err := db.lockedOne(`SELECT c.workspace,c.snapshot_operation,c.snapshot_id,c.stop_operation,
		c.status,c.created,c.updated FROM checkpointed_stops c
		JOIN workspaces w ON w.id=c.workspace
		WHERE c.workspace=? AND w.project=?`, workspace, project)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("checkpointed stop not found")
	}
	*dest = record
	return nil
}

func (db *Database) CheckpointStopStatus(project, workspace string) (map[string]any, error) {
	var result map[string]any
	err := db.transaction(func() error {
		return db.checkpointStopResultLocked(project, workspace, &result)
	})
	return result, err
}

// checkpointStopPendingLocked enforces write-quiescence while the snapshot is
// queued or running. Operation ordering ensures earlier commands finish first.
func (db *Database) checkpointStopPendingLocked(workspace string) (bool, error) {
	row, err := db.lockedOne(`SELECT status FROM checkpointed_stops WHERE workspace=?`, workspace)
	if err != nil {
		return false, err
	}
	return row != nil && asString(row["status"]) == "pending", nil
}

// finishCheckpointStopLocked is called only after FinishOperation has checked
// the worker lease and persisted the snapshot artifact and operation outcome.
func (db *Database) finishCheckpointStopLocked(ws, op map[string]any, succeeded bool, snapshotID string) error {
	action := asString(op["action"])
	id := asString(op["id"])
	workspace := asString(ws["id"])
	record, err := db.lockedOne(`SELECT status,snapshot_operation,stop_operation
		FROM checkpointed_stops WHERE workspace=?`, workspace)
	if err != nil || record == nil {
		return err
	}
	if action == "snapshot" && asString(record["snapshot_operation"]) == id &&
		asString(record["status"]) == "pending" {
		if !succeeded {
			return db.execLocked(`UPDATE checkpointed_stops SET status='failed',updated=?
				WHERE workspace=?`, stamp(), workspace)
		}
		if snapshotID == "" {
			return errors.New("verified checkpoint snapshot ID missing")
		}
		if asString(ws["status"]) != "ready" || asFloat(ws["expires"]) <= stamp() {
			return db.execLocked(`UPDATE checkpointed_stops SET status='expired',
				snapshot_id=?,updated=? WHERE workspace=?`, snapshotID, stamp(), workspace)
		}
		stopID, err := db.EnqueueLocked(workspace, asString(ws["node"]), "stop", map[string]any{})
		if err != nil {
			return err
		}
		now := stamp()
		if err := db.execLocked(`UPDATE workspaces SET status='stopping',updated=? WHERE id=?`, now, workspace); err != nil {
			return err
		}
		return db.execLocked(`UPDATE checkpointed_stops SET status='stopping',
			snapshot_id=?,stop_operation=?,updated=? WHERE workspace=?`,
			snapshotID, stopID, now, workspace)
	}
	if action == "stop" && asString(record["stop_operation"]) == id &&
		asString(record["status"]) == "stopping" {
		status := "failed_stop"
		if succeeded {
			status = "stopped"
		}
		return db.execLocked(`UPDATE checkpointed_stops SET status=?,updated=? WHERE workspace=?`,
			status, stamp(), workspace)
	}
	return nil
}
