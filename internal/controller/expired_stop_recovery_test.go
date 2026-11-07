package controller

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExpiredKubernetesStopRetriesAndPreservesLeasesUntilConfirmed(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateProject("demo", "Stop recovery"); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	now := stamp()
	if err := db.exec(
		"INSERT INTO workspaces(id,project,node,image,cpu,memory_mb,status,expires,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?)",
		id, "demo", "worker-one", "debian:bookworm-slim", 1.0, 512, "stopping", now+3600, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(
		"INSERT INTO hardware_leases(workspace,node,device_id,kind,acquired,released) VALUES(?,?,?,?,?,NULL)",
		id, "worker-one", "npu-0", "npu", now,
	); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := db.transaction(func() error {
		var err error
		original, err = db.EnqueueLocked(id, "worker-one", "stop", map[string]any{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(
		"UPDATE operations SET status='leased',token_hash='stale',lease_until=?,updated=? WHERE id=?",
		now-10, now-10, original,
	); err != nil {
		t.Fatal(err)
	}
	expire := func() {
		t.Helper()
		if err := db.transaction(func() error { return db.expireLocked(stamp()) }); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	expire()
	rows, err := db.query(
		"SELECT id,status FROM operations WHERE workspace=? AND action='stop' ORDER BY created,id", id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected one expired stop and one retry: %v %v", rows, err)
	}
	for _, row := range rows {
		if row["id"] == original && row["status"] != "expired" {
			t.Fatalf("original stop was not expired: %v", row)
		}
		if row["id"] != original && row["status"] != "queued" {
			t.Fatalf("stop retry was not queued: %v", row)
		}
	}
	leased, err := db.queryOne("SELECT released FROM hardware_leases WHERE workspace=?", id)
	if err != nil || leased == nil || leased["released"] != nil {
		t.Fatalf("device must remain reserved until verified stop: %v %v", leased, err)
	}
	claim, err := db.ClaimOperation("worker-one")
	if err != nil || asString(claim["action"]) != "stop" ||
		asString(claim["id"]) == original {
		t.Fatalf("worker could not reclaim idempotent stop: %v %v", claim, err)
	}
	_, err = db.FinishOperation("worker-one", asString(claim["id"]),
		asString(claim["lease_token"]), true, map[string]any{"status": "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := db.queryOne("SELECT status FROM workspaces WHERE id=?", id)
	if err != nil || workspace["status"] != "stopped" {
		t.Fatalf("workspace not confirmed stopped: %v %v", workspace, err)
	}
	lease, err := db.queryOne("SELECT released FROM hardware_leases WHERE workspace=?", id)
	if err != nil || lease["released"] == nil {
		t.Fatalf("hardware lease not released after confirmed stop: %v %v", lease, err)
	}
	expire()
	stops, err := db.query("SELECT id FROM operations WHERE workspace=? AND action='stop'", id)
	if err != nil || len(stops) != 2 {
		t.Fatalf("finished stops must never requeue: %v %v", stops, err)
	}
}
