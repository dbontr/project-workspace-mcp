package controller

import "testing"

func TestExternalResourcesRequireExactSelectionAndRemainOutsideFreePool(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.RegisterExternalResource("lab-fpga", "board", "ssh", "lab.example.org:22",
		"lab", "reservation required"); err != nil {
		t.Fatal(err)
	}
	resources, err := db.ExternalResources()
	if err != nil || len(resources) != 1 || resources[0]["id"] != "lab-fpga" {
		t.Fatalf("registry: %v %v", resources, err)
	}
	if _, err := db.RegisterExternalResource("guest-a", "eda-guest", "ssh", "root:password@lab:22",
		"lab", "restricted"); err == nil {
		t.Fatal("credential-bearing endpoint accepted")
	}
	personal, err := db.CreatePersonalTask("controller-owner", "FPGA synthesis", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID := asString(personal["id"])
	if _, err := db.ReserveExternalResource("lab-fpga", "task", taskID,
		"controller-owner", 30, false); err == nil {
		t.Fatal("external resource reserved implicitly")
	}
	if _, err := db.ReserveExternalResource("lab-fpga", "task", "missing",
		"controller-owner", 30, true); err == nil {
		t.Fatal("unknown personal task accepted")
	}
	claim, err := db.ReserveExternalResource("lab-fpga", "task", taskID,
		"controller-owner", 30, true)
	if err != nil || claim["status"] != "pending_external_adapter" ||
		claim["remote_lease_confirmed"] != false {
		t.Fatalf("local reservation: %v %v", claim, err)
	}
	if _, err := db.ReserveExternalResource("lab-fpga", "task", taskID,
		"controller-owner", 30, true); err == nil {
		t.Fatal("concurrent reservation accepted")
	}
	leases, err := db.ExternalResourceLeases("lab-fpga")
	if err != nil || len(leases) != 1 {
		t.Fatalf("lease history: %v %v", leases, err)
	}
	id := asString(claim["id"])
	if _, err := db.ReleaseExternalResource(id, false); err == nil {
		t.Fatal("implicit release accepted")
	}
	if _, err := db.ReleaseExternalResource(id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveExternalResource("lab-fpga", "task", taskID,
		"controller-owner", 30, true); err != nil {
		t.Fatalf("resource not reusable after explicit release: %v", err)
	}
}
