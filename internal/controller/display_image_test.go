package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func TestDisplayCaptureLifecycleAndMCPImage(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	token := pairWorker(t, srv)
	for _, request := range []WorkspaceRequest{
		{ProjectID: "demo", Display: true, DisplayWidth: 3840, DisplayHeight: 2160},
		{ProjectID: "demo", DisplayWidth: 1280},
	} {
		if _, err := db.CreateWorkspace(request); err == nil {
			t.Fatalf("accepted invalid display settings: %+v", request)
		}
	}

	created, err := db.CreateWorkspace(WorkspaceRequest{ProjectID: "demo", Display: true})
	if err != nil {
		t.Fatal(err)
	}
	ws := asString(created["id"])
	if created["display"] != true || asInt(created["display_width"]) != 1440 || asInt(created["display_height"]) != 900 {
		t.Fatalf("invalid default virtual display: %v", created)
	}
	start := claimOp(t, srv, token)
	startArgs := gitObj(start["payload"])
	if startArgs["display"] != true || asInt(startArgs["display_width"]) != 1440 {
		t.Fatalf("missing display configuration: %v", startArgs)
	}
	finishOp(t, srv, token, start, map[string]any{"status": "ready"})

	invoke := func(name string, args map[string]any) map[string]any {
		t.Helper()
		code, output := call(t, srv, "POST", "/mcp", testOwner, map[string]any{
			"jsonrpc": "2.0", "id": 7, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		wantOK(t, code, output)
		result := gitObj(output["result"])
		if result["isError"] == true {
			t.Fatalf("%s returned MCP error: %v", name, result)
		}
		return result
	}
	capture := invoke("workspace_capture_display", map[string]any{"project_id": "demo", "workspace_id": ws})
	queued := gitObj(capture["structuredContent"])
	op := claimOp(t, srv, token)
	if op["id"] != queued["operation_id"] || op["action"] != "capture_display" {
		t.Fatalf("unexpected capture operation: %v", op)
	}

	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 640, 480))); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(imageData.Bytes())
	req, err := http.NewRequest("PUT",
		srv.URL+"/api/worker/worker-one/workspaces/"+ws+"/operations/"+asString(op["id"])+"/artifact",
		bytes.NewReader(imageData.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Operation-Token", asString(op["lease_token"]))
	req.Header.Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	req.Header.Set("X-Artifact-Name", "display.png")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("screenshot upload rejected: %d", resp.StatusCode)
	}
	var uploaded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	finishOp(t, srv, token, op, map[string]any{
		"artifact_id": uploaded["id"], "width": 640, "height": 480, "mime_type": "image/png",
	})
	record, err := db.Operation("demo", ws, asString(op["id"]))
	if err != nil || record["status"] != "succeeded" {
		t.Fatalf("capture did not finish durably: %v %v", record, err)
	}
	result := gitObj(record["result"])
	artifact := gitObj(result["artifact"])
	if artifact["id"] != uploaded["id"] || asInt(result["width"]) != 640 {
		t.Fatalf("missing PNG artifact metadata: %v", record)
	}

	viewed := invoke("artifact_view_image", map[string]any{
		"project_id": "demo", "artifact_id": uploaded["id"],
	})
	parts := viewed["content"].([]any)
	imagePart := gitObj(parts[0])
	if imagePart["type"] != "image" || imagePart["mimeType"] != "image/png" {
		t.Fatalf("MCP did not return image content: %v", imagePart)
	}
	decoded, err := base64.StdEncoding.DecodeString(asString(imagePart["data"]))
	if err != nil || !bytes.Equal(decoded, imageData.Bytes()) {
		t.Fatalf("MCP image bytes differ from stored artifact: %v", err)
	}
	if _, err := db.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadPNGImage("other", asString(uploaded["id"])); err == nil {
		t.Fatal("cross-project screenshot disclosure")
	}
}

func TestDisplayImageRejectsMalformedData(t *testing.T) {
	db, _, srv := fixture(t)
	project(t, srv)
	meta, err := db.ArtifactSave("demo", "display.png", []byte("not a PNG"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadPNGImage("demo", asString(meta["id"])); err == nil ||
		!strings.Contains(err.Error(), "PNG") {
		t.Fatalf("malformed image accepted: %v", err)
	}
}
