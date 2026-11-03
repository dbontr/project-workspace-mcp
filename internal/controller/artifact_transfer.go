package controller

import (
	"encoding/base64"
	"errors"
	"os"
)

const maxMCPArtifactBytes = 512 << 10

// StoreMCPArtifact is for small attachments encoded inside a JSON-RPC call.
// Larger files use the authenticated streamed artifact upload endpoint.
func (db *Database) StoreMCPArtifact(project, name, digest, encoded string) (map[string]any, error) {
	if !validArtifactName.MatchString(name) || !shaPattern.MatchString(digest) {
		return nil, errors.New("invalid artifact name or SHA-256")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxMCPArtifactBytes) {
		return nil, errors.New("MCP attachment exceeds 512 KiB; use streamed upload")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > maxMCPArtifactBytes {
		return nil, errors.New("invalid or excessive base64 attachment")
	}
	if checksum(data) != digest {
		return nil, errors.New("artifact checksum mismatch")
	}
	return db.ArtifactSave(project, name, data)
}

// ReadMCPArtifact returns small data inline after verifying its disk checksum.
// Larger artifacts are downloaded over the existing authenticated REST route.
func (db *Database) ReadMCPArtifact(project, id string) (map[string]any, error) {
	meta, err := db.ArtifactInfo(project, id)
	if err != nil {
		return nil, err
	}
	if asFloat(meta["size"]) > maxMCPArtifactBytes {
		return nil, errors.New("artifact exceeds 512 KiB; use authenticated download")
	}
	path, err := db.VerifyArtifact(meta)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxMCPArtifactBytes || checksum(data) != asString(meta["sha256"]) {
		return nil, errors.New("artifact integrity check failed")
	}
	return map[string]any{
		"id": id, "name": meta["name"], "size": len(data),
		"sha256": meta["sha256"], "data_base64": base64.StdEncoding.EncodeToString(data),
	}, nil
}
