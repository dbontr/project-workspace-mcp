package controller

import "errors"

// PruneWorkspaceVolume queues removal of the retained Docker volume only
// after a successful, checksum-verified checkpoint-before-stop transaction.
// The worker separately validates the volume's ownership labels before removal.
func (db *Database) PruneWorkspaceVolume(project, workspace, snapshot string, confirm bool) (map[string]any, error) {
	if !confirm || !validID.MatchString(project) ||
		!contributionArtifactID.MatchString(workspace) ||
		!contributionArtifactID.MatchString(snapshot) {
		return nil, errors.New("volume removal requires explicit confirmation and exact identifiers")
	}
	artifact, err := db.queryOne(`SELECT a.id,a.name,a.sha256,a.size FROM workspaces w
		JOIN checkpointed_stops c ON c.workspace=w.id
		JOIN workspace_snapshots s ON s.workspace=w.id AND s.id=c.snapshot_id
		JOIN artifacts a ON a.id=s.artifact AND a.project=w.project
		WHERE w.project=? AND w.id=? AND s.id=? AND w.status='stopped'
			AND c.status='stopped'`, project, workspace, snapshot)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, errors.New("stopped workspace and matching durable checkpoint required")
	}
	if _, err := db.VerifyArtifact(artifact); err != nil {
		return nil, err
	}
	var operation string
	err = db.transaction(func() error {
		row, err := db.lockedOne(`SELECT w.node FROM workspaces w
			JOIN checkpointed_stops c ON c.workspace=w.id
			JOIN workspace_snapshots s ON s.workspace=w.id AND s.id=c.snapshot_id
			JOIN artifacts a ON a.id=s.artifact AND a.project=w.project
			WHERE w.project=? AND w.id=? AND s.id=? AND w.status='stopped'
				AND c.status='stopped'`, project, workspace, snapshot)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("workspace checkpoint or stop status changed")
		}
		active, err := db.countLocked(`SELECT count(*) n FROM hardware_leases
			WHERE workspace=? AND released IS NULL`, workspace)
		if err != nil {
			return err
		}
		if active > 0 {
			return errors.New("workspace still retains hardware leases")
		}
		pending, err := db.countLocked(`SELECT count(*) n FROM operations
			WHERE workspace=? AND status IN ('queued','leased')`, workspace)
		if err != nil {
			return err
		}
		if pending > 0 {
			return errors.New("workspace has unfinished operations")
		}
		existing, err := db.countLocked(`SELECT count(*) n FROM operations
			WHERE workspace=? AND action='prune_volume' AND status='succeeded'`, workspace)
		if err != nil {
			return err
		}
		if existing > 0 {
			return errors.New("workspace volume was already pruned")
		}
		operation, err = db.EnqueueLocked(workspace, asString(row["node"]), "prune_volume",
			map[string]any{"snapshot_id": snapshot, "sha256": artifact["sha256"]})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": operation, "status": "queued",
		"snapshot_id": snapshot}, nil
}
