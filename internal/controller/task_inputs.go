package controller

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"strings"
	"unicode/utf8"
)

// AdmitTaskAttachment imports bytes supplied by an authorized MCP client.
// The client must send the actual bytes; the server cannot resolve native
// ChatGPT attachment references from a filename or message alone.
func (db *Database) AdmitTaskAttachment(owner, task, name, mediaType, digest, data string, revision int) (map[string]any, error) {
	if !validArtifactName.MatchString(name) || revision < 0 || !shaPattern.MatchString(digest) {
		return nil, errors.New("invalid attachment name, revision or checksum")
	}
	parsed, params, err := mime.ParseMediaType(mediaType)
	if err != nil || len(params) > 0 || len(parsed) > 96 ||
		!(strings.HasPrefix(parsed, "text/") || strings.HasPrefix(parsed, "image/") ||
			parsed == "application/pdf" || parsed == "application/json" ||
			parsed == "application/octet-stream") {
		return nil, errors.New("unsupported attachment media type")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(raw) == 0 || len(raw) > maxMCPArtifactBytes ||
		checksum(raw) != digest {
		return nil, errors.New("attachment content checksum mismatch or size limit exceeded")
	}
	if parsed == "application/pdf" && !strings.HasPrefix(string(raw), "%PDF-") {
		return nil, errors.New("attachment is not a PDF")
	}
	if strings.HasPrefix(parsed, "text/") && (!utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0) {
		return nil, errors.New("text attachment is not valid UTF-8")
	}
	record, err := db.SavePersonalTaskBase64(owner, task, name, digest, data, revision)
	if err != nil {
		return nil, err
	}
	record["media_type"] = parsed
	record["parsed"] = false
	record["scope"] = "task"
	return record, nil
}

type StudyPackSource struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SHA256   string `json:"sha256"`
	Location string `json:"location"`
}

type StudyPackQuestion struct {
	Prompt    string   `json:"prompt"`
	Answer    string   `json:"answer"`
	SourceIDs []string `json:"source_ids"`
}

type StudyPack struct {
	Title     string              `json:"title"`
	Objective string              `json:"objective"`
	Sources   []StudyPackSource   `json:"sources"`
	Questions []StudyPackQuestion `json:"questions"`
}

// SaveTaskStudyPack accepts a prepared pack with internally consistent source
// references. An unverified client-provided source cannot be called verified.
func (db *Database) SaveTaskStudyPack(owner, task, name string, pack StudyPack, revision int) (map[string]any, error) {
	if !strings.HasSuffix(name, ".json") || !validArtifactName.MatchString(name) ||
		revision < 0 || strings.TrimSpace(pack.Title) == "" || len(pack.Title) > 200 ||
		len(pack.Objective) > 2048 || len(pack.Sources) == 0 || len(pack.Sources) > 128 ||
		len(pack.Questions) == 0 || len(pack.Questions) > 128 {
		return nil, errors.New("invalid study pack")
	}
	sources := map[string]bool{}
	for _, src := range pack.Sources {
		if !validArtifactName.MatchString(src.ID) || sources[src.ID] ||
			strings.TrimSpace(src.Title) == "" || len(src.Title) > 200 ||
			!shaPattern.MatchString(src.SHA256) || src.Location == "" ||
			len(src.Location) > 512 {
			return nil, errors.New("study pack has an invalid source reference")
		}
		sources[src.ID] = true
	}
	for _, q := range pack.Questions {
		if strings.TrimSpace(q.Prompt) == "" || len(q.Prompt) > 4096 ||
			strings.TrimSpace(q.Answer) == "" || len(q.Answer) > 8192 ||
			len(q.SourceIDs) == 0 || len(q.SourceIDs) > 12 {
			return nil, errors.New("study pack question is incomplete")
		}
		for _, id := range q.SourceIDs {
			if !sources[id] {
				return nil, errors.New("study pack question refers to an unknown source")
			}
		}
	}
	data, err := json.Marshal(pack)
	if err != nil || len(data) > maxMCPArtifactBytes {
		return nil, errors.New("study pack exceeds size limit")
	}
	record, err := db.SavePersonalTaskText(owner, task, name, string(data), revision)
	if err != nil {
		return nil, err
	}
	record["content_type"] = "application/json"
	record["scope"] = "task"
	record["source_provenance_verified"] = false
	return record, nil
}
