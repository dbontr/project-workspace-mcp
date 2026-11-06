package controller

import (
	"net/http"
	"testing"
)

func TestInterruptedWorkerReconciliationFencesOldClaims(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	workerToken := pairWorker(t, srv)
	capacity := caps()
	capacity["max_jobs"] = 1
	if err := db.Heartbeat("worker-one", capacity); err != nil {
		t.Fatal(err)
	}
	first, err := db.SubmitJob("demo", "sleep 60", "debian:bookworm-slim", 1, 256)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SubmitJob("demo", "echo ready", "debian:bookworm-slim", 1, 256)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := db.ClaimJob("worker-one")
	if err != nil || asString(leased["id"]) != asString(first["id"]) {
		t.Fatalf("first job not leased: %v %v", leased, err)
	}
	oldToken := asString(leased["lease_token"])
	if next, err := db.ClaimJob("worker-one"); err != nil || next != nil {
		t.Fatalf("abandoned lease incorrectly released before cleanup: %v %v", next, err)
	}
	endpoint := "/api/worker/worker-one/reconcile"
	if status, _ := call(t, srv, http.MethodPost, endpoint, testOwner, nil); status != http.StatusUnauthorized {
		t.Fatalf("administrator credential must not impersonate a worker: HTTP %d", status)
	}
	status, result := call(t, srv, http.MethodPost, endpoint, workerToken, nil)
	wantOK(t, status, result)
	if asInt(result["expired_jobs"]) != 1 || asString(result["node"]) != "worker-one" {
		t.Fatalf("unexpected reconciliation result: %v", result)
	}
	if _, err := db.FinishJob("worker-one", asString(first["id"]), oldToken, "succeeded", "late"); err == nil {
		t.Fatal("abandoned job token was accepted after fencing")
	}
	jobs, err := db.Jobs("demo")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, job := range jobs {
		if job["id"] == first["id"] {
			found = true
			if job["status"] != "expired" {
				t.Fatalf("interrupted job was not expired: %v", job)
			}
		}
	}
	if !found {
		t.Fatal("interrupted job missing from durable history")
	}
	next, err := db.ClaimJob("worker-one")
	if err != nil || asString(next["id"]) != asString(second["id"]) {
		t.Fatalf("worker capacity did not recover: %v %v", next, err)
	}
	status, result = call(t, srv, http.MethodPost, endpoint, workerToken, nil)
	wantOK(t, status, result)
	if asInt(result["expired_jobs"]) != 1 {
		t.Fatalf("active lease must be fenced on the next verified reconnect: %v", result)
	}
	status, result = call(t, srv, http.MethodPost, endpoint, workerToken, nil)
	wantOK(t, status, result)
	if asInt(result["expired_jobs"]) != 0 {
		t.Fatalf("reconciliation was not idempotent: %v", result)
	}
}
