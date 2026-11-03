package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVirtualDisplayBounds(t *testing.T) {
	for _, size := range [][2]int{{0, 900}, {319, 900}, {640, 239}, {3841, 500}, {1920, 2161}, {3840, 2160}} {
		if validDisplaySize(size[0], size[1]) {
			t.Fatalf("accepted invalid display %dx%d", size[0], size[1])
		}
	}
	for _, size := range [][2]int{{320, 240}, {1440, 900}, {1920, 1080}} {
		if !validDisplaySize(size[0], size[1]) {
			t.Fatalf("rejected valid display %dx%d", size[0], size[1])
		}
	}
}

func TestCaptureDisplayUploadsVerifiedPNG(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 640, 480))); err != nil {
		t.Fatal(err)
	}
	pngData := original.Bytes()
	expected := sha256.Sum256(pngData)
	tmp := t.TempDir()
	imageFile := filepath.Join(tmp, "screen.png")
	if err := os.WriteFile(imageFile, pngData, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tmp, "docker.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = exec ] && [ \"$3\" = xdpyinfo ]; then printf 'dimensions: 640x480 pixels\\n'; exit 0; fi\n" +
		"if [ \"$1\" = exec ] && [ \"$3\" = ffmpeg ]; then cat " + shellQuote(imageFile) + "; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(tmp, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	uploaded := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "PUT" || !strings.HasSuffix(req.URL.Path, "/artifact") {
			t.Errorf("unexpected upload: %s %s", req.Method, req.URL.Path)
			http.Error(w, "bad request", 400)
			return
		}
		if req.Header.Get("X-Operation-Token") != "lease" ||
			req.Header.Get("X-Artifact-Name") != "display.png" ||
			req.Header.Get("X-Content-SHA256") != hex.EncodeToString(expected[:]) {
			t.Error("invalid screenshot artifact upload headers")
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(req.Body)
		if !bytes.Equal(body.Bytes(), pngData) {
			t.Error("screenshot upload differs from captured PNG")
		}
		uploaded = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": strings.Repeat("f", 32), "sha256": hex.EncodeToString(expected[:]),
		})
	}))
	defer api.Close()
	client := &Client{Base: api.URL, Node: "worker-one", Token: "worker-token", HTTP: api.Client()}
	runner := NewRunner(nil)
	result, err := runner.captureDisplay(context.Background(), Operation{
		ID: strings.Repeat("a", 32), Workspace: strings.Repeat("b", 32), LeaseToken: "lease",
	}, client)
	if err != nil || !uploaded {
		t.Fatalf("display capture not uploaded: %v %v", result, err)
	}
	if result["artifact_id"] != strings.Repeat("f", 32) || result["width"] != 640 || result["height"] != 480 {
		t.Fatalf("invalid screenshot receipt: %v", result)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "ffmpeg -hide_banner -loglevel error -nostdin -f x11grab -video_size 640x480") {
		t.Fatalf("capture did not use isolated container display: %s", logged)
	}
}

func TestCaptureDisplayRejectsUnavailableDisplay(t *testing.T) {
	tmp := t.TempDir()
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(filepath.Join(tmp, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := NewRunner(nil).captureDisplay(context.Background(),
		Operation{Workspace: strings.Repeat("c", 32)}, &Client{})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing display must fail before upload: %v", err)
	}
}
