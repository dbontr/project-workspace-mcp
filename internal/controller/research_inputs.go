package controller

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
)

// StoreMCPResearchAttachment accepts bytes supplied by an authenticated MCP
// client; a chat attachment is inaccessible until the client transmits it.
func (db *Database) StoreMCPResearchAttachment(library, name, kind, digest, encoded string) (map[string]any, error) {
	if !shaPattern.MatchString(digest) || len(encoded) > (maxMCPArtifactBytes*4/3)+8 {
		return nil, errors.New("invalid attachment digest or MCP size")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxMCPArtifactBytes {
		return nil, errors.New("attachment must contain at most 512 KiB")
	}
	if checksum(data) != digest {
		return nil, errors.New("attachment SHA-256 mismatch")
	}
	return db.StoreLibraryObject(library, name, kind, digest, bytes.NewReader(data), maxMCPArtifactBytes)
}

// PrepareStudyPack composes a checked, source-linked packet for the caller.
// It does not write project knowledge or claim to have extracted PDF pages.
func (db *Database) PrepareStudyPack(project, title, objective string, ids, questions []string) (map[string]any, error) {
	if !validID.MatchString(project) || strings.TrimSpace(title) == "" ||
		len(title) > 160 || len(objective) > 2048 || len(ids) == 0 ||
		len(ids) > 48 || len(questions) > 64 {
		return nil, errors.New("invalid study pack request")
	}
	for _, q := range questions {
		if strings.TrimSpace(q) == "" || len(q) > 2000 {
			return nil, errors.New("invalid study question")
		}
	}
	items := make([]map[string]any, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, errors.New("repeated study passage ID")
		}
		seen[id] = true
		passage, err := db.ReadResearchPassage(project, id)
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"citation_ref": passage["citation_ref"], "source_id": passage["source"],
			"title": passage["title"], "original": passage["origin_name"],
			"original_sha256":     passage["origin_digest"],
			"repository_revision": passage["origin_revision"],
			"page":                passage["page"], "section": passage["section"],
			"line_start": passage["line_start"], "line_end": passage["line_end"],
			"body": passage["body"], "body_sha256": passage["body_sha256"],
		})
	}
	pack := map[string]any{"title": title, "objective": objective, "project": project,
		"passages": items, "questions": questions, "stored": false, "source_verified": true}
	pack["sha256"] = checksum([]byte(encode(pack)))
	return pack, nil
}
