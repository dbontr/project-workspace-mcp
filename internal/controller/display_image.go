package controller

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"os"
)

const maxMCPDisplayImageBytes = 4 << 20

// PNGImageResult contains MCP image content and safe, compact artifact metadata.
type PNGImageResult struct {
	Data     string
	Metadata map[string]any
}

// ReadPNGImage verifies project access, artifact integrity, PNG structure,
// dimensions, and size before returning an image for model inspection.
func (db *Database) ReadPNGImage(project, id string) (*PNGImageResult, error) {
	meta, err := db.ArtifactInfo(project, id)
	if err != nil {
		return nil, err
	}
	if asFloat(meta["size"]) <= 0 || asFloat(meta["size"]) > maxMCPDisplayImageBytes {
		return nil, errors.New("image exceeds MCP inline limit; use authenticated artifact download")
	}
	location, err := db.VerifyArtifact(meta)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxMCPDisplayImageBytes {
		return nil, errors.New("invalid image size")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 ||
		config.Width > 3840 || config.Height > 2160 || config.Width*config.Height > 8_000_000 {
		return nil, errors.New("invalid PNG image dimensions")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("invalid PNG data: %w", err)
	}
	return &PNGImageResult{
		Data: base64.StdEncoding.EncodeToString(data),
		Metadata: map[string]any{
			"artifact_id": id, "width": config.Width, "height": config.Height,
			"mime_type": "image/png", "sha256": meta["sha256"],
		},
	}, nil
}
