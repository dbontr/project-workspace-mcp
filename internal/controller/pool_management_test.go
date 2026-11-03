package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProgramReviewWithoutNativeExecution(t *testing.T) {
	db, _, _ := fixture(t)
	request := ProgramProposal{
		ID: "ltspice", Name: "LTspice", Version: "26",
		Platform: "windows-native", Distribution: "native",
		SourceURL:    "https://www.analog.com/en/resources/design-tools-and-calculators/ltspice-simulator.html",
		LicenseNotes: "Operator must verify license and installation terms before use",
	}
	proposed, err := db.ProposeProgram(request)
	if err != nil || proposed["status"] != "requested" {
		t.Fatalf("proposal: %v %v", proposed, err)
	}
	if _, err := db.ProposeProgram(request); err == nil {
		t.Fatal("duplicate program ID must not overwrite a proposal")
	}
	if _, err := db.ReviewProgram("ltspice", "available", "install now", "", "", 1); err == nil {
		t.Fatal("native program cannot be activated without review and an adapter")
	}
	review, err := db.ReviewProgram("ltspice", "reviewed", "Vendor documentation checked; installation still unavailable", "", "", 1)
	if err != nil || review["status"] != "reviewed" {
		t.Fatalf("review: %v %v", review, err)
	}
	if _, err := db.ReviewProgram("ltspice", "available", "approved", "image", "windows", 2); err == nil {
		t.Fatal("native application cannot be represented as container-ready")
	}
	if _, err := db.ReviewProgram("ltspice", "retired", "No longer requested", "", "", 1); err == nil {
		t.Fatal("stale expected revision must fail")
	}
	retired, err := db.ReviewProgram("ltspice", "retired", "No longer requested", "", "", 2)
	if err != nil || retired["status"] != "retired" {
		t.Fatalf("retire: %v %v", retired, err)
	}
	if _, err := db.ReviewProgram("ltspice", "reviewed", "Reopen", "", "", 3); err == nil {
		t.Fatal("retired program cannot be reactivated")
	}
	events, err := db.PoolEvents("program", "ltspice")
	if err != nil || len(events) != 3 {
		t.Fatalf("missing proposal and review audit: %v %v", events, err)
	}
	if events[0]["status"] != "retired" {
		t.Fatalf("wrong latest event: %v", events[0])
	}
}

func TestPoolStateBlocksFutureAllocationsAndPreservesRecords(t *testing.T) {
	db, _, httpServer := fixture(t)
	project(t, httpServer)
	if _, err := db.RegisterPoolImage("ubuntu-base", "ubuntu:24.04", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterPoolSoftware("spice", "ubuntu-base", "SPICE simulation", []string{"ngspice"}); err != nil {
		t.Fatal(err)
	}
	request := ProgramProposal{
		ID: "ngspice", Name: "ngspice", Platform: "linux-container", Distribution: "apt",
		SourceURL: "https://ngspice.sourceforge.io/", LicenseNotes: "Open source license reviewed",
	}
	if _, err := db.ProposeProgram(request); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReviewProgram("ngspice", "reviewed", "License and package confirmed", "", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReviewProgram("ngspice", "available", "Approved Ubuntu package", "software", "spice", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnvironmentFromPool("demo", "spice-env", "ubuntu-base", []string{"spice"}, 0); err != nil {
		t.Fatal(err)
	}
	state, err := db.SetPoolState("software", "spice", "disabled", 1, "Package review pending")
	if err != nil || state["revision"] != 2 {
		t.Fatalf("disable: %v %v", state, err)
	}
	if _, err := db.EnvironmentFromPool("demo", "spice-env-next", "ubuntu-base", []string{"spice"}, 0); err == nil {
		t.Fatal("disabled software must not compose a new environment")
	}
	if _, err := db.SetPoolState("software", "spice", "active", 1, "Stale"); err == nil {
		t.Fatal("stale revision cannot reactivate software")
	}
	if _, err := db.SetPoolState("software", "spice", "active", 2, "Package approved again"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnvironmentFromPool("demo", "spice-env-next", "ubuntu-base", []string{"spice"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetPoolState("image", "ubuntu-base", "disabled", 1, "Image no longer approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnvironmentFromPool("demo", "blocked-env", "ubuntu-base", []string{"spice"}, 0); err == nil {
		t.Fatal("disabled base image must deny new environments")
	}
	if _, err := db.RegisterPoolSoftware("new-software", "ubuntu-base", "", []string{"gcc"}); err == nil {
		t.Fatal("disabled image cannot accept new apt package collections")
	}
	caps := caps()
	caps["images"] = []any{"ubuntu:24.04"}
	if _, err := db.RegisterNode("worker-one", caps); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", ImagePoolID: "ubuntu-base"}); err == nil {
		t.Fatal("disabled pool image cannot allocate a new workspace")
	}
	images, err := db.ImagePool()
	if err != nil || len(images) != 1 || images[0]["status"] != "disabled" || len(images[0]["available_nodes"].([]string)) != 0 {
		t.Fatalf("disabled image availability: %v %v", images, err)
	}
	if _, err := db.SetPoolState("image", "ubuntu-base", "retired", 2, "Retired from catalog"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetPoolState("image", "ubuntu-base", "active", 3, "Reinstate"); err == nil {
		t.Fatal("retired image must remain retired")
	}
	row, err := db.queryOne("SELECT base_image FROM environments WHERE project='demo' AND name='spice-env'")
	if err != nil || row == nil || row["base_image"] != "ubuntu:24.04" {
		t.Fatalf("existing environment must remain stored: %v %v", row, err)
	}
}

func TestCatalogManagementRESTAndMCPAccess(t *testing.T) {
	_, server, httpServer := fixture(t)
	project(t, httpServer)
	code, _ := call(t, httpServer, http.MethodPost, "/api/pool/programs", "", ProgramProposal{
		ID: "ltspice", Name: "LTspice", Platform: "windows-native", Distribution: "native",
		SourceURL: "https://www.analog.com/en/resources/design-tools-and-calculators/ltspice-simulator.html",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("pool mutation must authenticate, got %d", code)
	}
	code, proposal := call(t, httpServer, http.MethodPost, "/api/pool/programs", testOwner, ProgramProposal{
		ID: "ltspice", Name: "LTspice", Platform: "windows-native", Distribution: "native",
		SourceURL:    "https://www.analog.com/en/resources/design-tools-and-calculators/ltspice-simulator.html",
		LicenseNotes: "Needs vendor license review",
	})
	wantOK(t, code, proposal)
	if proposal["status"] != "requested" {
		t.Fatal("creating a request cannot install software")
	}
	value, err := server.callTool("pool_list_programs", nil)
	if err != nil || len(value.([]map[string]any)) != 1 {
		t.Fatalf("MCP program listing: %v %v", value, err)
	}
	code, result := call(t, httpServer, http.MethodPost, "/api/pool/programs/ltspice/review", testOwner,
		map[string]any{"status": "reviewed", "expected_revision": 1, "review_note": "Distribution reviewed"})
	wantOK(t, code, result)
	if result["status"] != "reviewed" {
		t.Fatalf("review unexpected: %v", result)
	}
	if _, err := server.callTool("pool_review_program", map[string]any{
		"id": "ltspice", "status": "available", "expected_revision": 2, "review_note": "No adapter",
	}); err == nil {
		t.Fatal("native program cannot become available")
	}
	if _, err := server.callTool("pool_events", map[string]any{"kind": "program", "id": "ltspice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.callTool("pool_propose_program", map[string]any{
		"id": "bad", "name": "bad", "platform": "windows-native", "distribution": "native",
		"source_url": "http://localhost/installer.exe",
	}); err == nil {
		t.Fatal("untrusted installer URLs must be rejected")
	}
}

func TestPoolStatusMalformedInputs(t *testing.T) {
	db, _, _ := fixture(t)
	if _, err := db.RegisterPoolImage("base-image", "debian:bookworm-slim", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct {
		kind, id, status, reason string
		version                  int
	}{
		{"unknown", "base-image", "disabled", "test", 1},
		{"image", "base-image", "active", "test", 1},
		{"image", "base-image", "disabled", "", 1},
		{"image", "base-image", "disabled", "test", 0},
		{"image", "not-present", "disabled", "test", 1},
	} {
		if _, err := db.SetPoolState(req.kind, req.id, req.status, req.version, req.reason); err == nil {
			t.Fatalf("unexpected accepted pool state change: %#v", req)
		}
	}
	if _, err := db.ProposeProgram(ProgramProposal{
		ID: "invalid-native", Name: "Invalid", Platform: "macos-native",
		Distribution: "apt", SourceURL: "https://example.org/",
	}); err == nil {
		t.Fatal("unsupported native apt setup must be rejected")
	}
}

func TestCatalogProgramsListedWithStableMetadata(t *testing.T) {
	_, server, httpServer := fixture(t)
	project(t, httpServer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/pool/programs", nil)
	request.Host = "127.0.0.1"
	request.Header.Set("Authorization", "Bearer "+testOwner)
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("pool list failed: %d %s", response.Code, response.Body.String())
	}
}
