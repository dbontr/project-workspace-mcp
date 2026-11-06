package controller

import (
	"math"
	"testing"
)

func TestNonFiniteCPURequestsCannotEnterJobQueue(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.CreateProject("research", "Research"); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreatePersonalTask("controller-owner", "Compilation", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cpu := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := db.SubmitJob("research", "echo ok", "debian:bookworm-slim", cpu, 512); err == nil {
			t.Fatalf("project job admitted non-finite CPU quantity %v", cpu)
		}
		if _, err := db.SubmitPersonalTaskJob("controller-owner", asString(task["id"]),
			"echo ok", "debian:bookworm-slim", cpu, 512); err == nil {
			t.Fatalf("independent job admitted non-finite CPU quantity %v", cpu)
		}
	}
	row, err := db.queryOne("SELECT count(*) n FROM jobs")
	if err != nil || asInt(row["n"]) != 0 {
		t.Fatalf("rejected requests entered job queue: %v %v", row, err)
	}
}
