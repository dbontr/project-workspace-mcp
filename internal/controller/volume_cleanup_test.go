package controller

import (
	"encoding/base64"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestGuardedVolumePruningRequiresRestorableCheckpoint(t *testing.T) {
	db, server, srv := fixture(t)
	project(t, srv)
	workerToken := pairWorker(t, srv)
	code, created := call(t, srv, http.MethodPost, "/api/workspaces", testOwner,
		map[string]any{"project_id": "demo", "cpu": 1, "memory_mb": 512})
	wantOK(t, code, created)
	workspace := asString(created["id"])
	finishOp(t, srv, workerToken, claimOp(t, srv, workerToken), map[string]any{"status": "ready"})

	if _, err := db.PruneWorkspaceVolume("demo", workspace, "00000000000000000000000000000000", true); err == nil {
		t.Fatal("active workspace must not be pruned")
	}
	if _, err := db.CheckpointStopWorkspace("demo", workspace); err != nil {
		t.Fatal(err)
	}
	snapshot := claimOp(t, srv, workerToken)
	payload := []byte("restorable checkpoint contents")
	finishOp(t, srv, workerToken, snapshot, map[string]any{
		"name": "workspace.tar.gz", "base64": base64.StdEncoding.EncodeToString(payload),
	})
	stop := claimOp(t, srv, workerToken)
	if stop["action"] != "stop" {
		t.Fatalf("checkpoint must queue teardown: %v", stop)
	}
	finishOp(t, srv, workerToken, stop, map[string]any{"status": "stopped"})
	info, err := db.CheckpointStopStatus("demo", workspace)
	if err != nil || info["status"] != "stopped" {
		t.Fatalf("checkpoint was not completed: %v %v", info, err)
	}
	snapshotID := asString(info["snapshot_id"])
	for _, attempt := range []struct {
		project, snapshot string
		confirm           bool
	}{
		{"demo", snapshotID, false},
		{"other", snapshotID, true},
		{"demo", "00000000000000000000000000000000", true},
	} {
		if _, err := db.PruneWorkspaceVolume(attempt.project, workspace, attempt.snapshot, attempt.confirm); err == nil {
			t.Fatalf("accepted unauthorized or unmatched volume cleanup: %+v", attempt)
		}
	}
	snapshots, err := db.SnapshotList("demo", workspace)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("no durable checkpoint: %v %v", snapshots, err)
	}
	artifactID := asString(snapshots[0]["artifact"])
	meta, err := db.ArtifactInfo("demo", artifactID)
	if err != nil {
		t.Fatal(err)
	}
	file := db.artifactPath(artifactID)
	if err := os.WriteFile(file, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneWorkspaceVolume("demo", workspace, snapshotID, true); err == nil {
		t.Fatal("corrupted checkpoint must prevent data deletion")
	}
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.VerifyArtifact(meta); err != nil {
		t.Fatalf("restored checkpoint integrity: %v", err)
	}

	key, issuer := testOIDCIssuer(t, server)
	principal := "oidc:" + issuer + "#researcher"
	userToken := signedTestToken(t, key, issuer, "project-workspace",
		"researcher", time.Now().Add(time.Hour).Unix())
	if _, err := db.SetProjectGrant("demo", principal, "editor", ""); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/demo/workspaces/" + workspace + "/volume/prune"
	code, _ = call(t, srv, http.MethodPost, path, userToken,
		map[string]any{"snapshot_id": snapshotID, "confirm": true})
	if code != http.StatusForbidden {
		t.Fatalf("editor can prune a workspace volume: HTTP %d", code)
	}
	if _, err := db.SetProjectGrant("demo", principal, "owner", "editor"); err != nil {
		t.Fatal(err)
	}
	code, queued := call(t, srv, http.MethodPost, path, userToken,
		map[string]any{"snapshot_id": snapshotID, "confirm": true})
	wantOK(t, code, queued)
	if queued["status"] != "queued" || queued["snapshot_id"] != snapshotID {
		t.Fatalf("missing cleanup operation: %v", queued)
	}
	if _, err := db.PruneWorkspaceVolume("demo", workspace, snapshotID, true); err == nil {
		t.Fatal("allowed duplicate cleanup while operation is queued")
	}
	prune := claimOp(t, srv, workerToken)
	if prune["action"] != "prune_volume" ||
		prune["id"] != queued["operation_id"] {
		t.Fatalf("unexpected cleanup operation: %v", prune)
	}
	finishOp(t, srv, workerToken, prune, map[string]any{"status": "pruned"})
	if _, err := db.PruneWorkspaceVolume("demo", workspace, snapshotID, true); err == nil {
		t.Fatal("allowed cleanup of already-pruned volume")
	}
}
