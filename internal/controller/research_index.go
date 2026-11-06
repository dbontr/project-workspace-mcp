package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

type ResearchSourceRequest struct {
	Library        string `json:"library"`
	Title          string `json:"title"`
	OriginKind     string `json:"origin_kind"`
	OriginName     string `json:"origin_name"`
	OriginRevision string `json:"origin_revision"`
	OriginPath     string `json:"origin_path"`
}

type ResearchPassageInput struct {
	Ordinal   int      `json:"ordinal"`
	Page      int      `json:"page"`
	Section   string   `json:"section"`
	LineStart int      `json:"line_start"`
	LineEnd   int      `json:"line_end"`
	Text      string   `json:"text"`
	Topics    []string `json:"topics"`
}

var researchTopic = regexp.MustCompile(`^[a-z][a-z0-9-]{0,48}$`)
var researchTerms = regexp.MustCompile(`[\pL\pN]{2,}`)

func (db *Database) RegisterResearchSource(req ResearchSourceRequest) (map[string]any, error) {
	if !validID.MatchString(req.Library) || strings.TrimSpace(req.Title) == "" ||
		len(req.Title) > 200 || (req.OriginKind != "object" && req.OriginKind != "git") {
		return nil, errors.New("invalid research source metadata")
	}
	if req.OriginKind == "object" {
		if !validArtifactName.MatchString(req.OriginName) || req.OriginRevision != "" || req.OriginPath != "" {
			return nil, errors.New("object source requires one immutable library object")
		}
	} else if !validSourceRepository(req.OriginName) ||
		!validCommitSHA.MatchString(req.OriginRevision) ||
		req.OriginPath != "" && !validRelativePath(req.OriginPath) {
		return nil, errors.New("Git source requires a pinned repository revision and path")
	}
	id, now := randomHex(16), stamp()
	var digest string
	err := db.transaction(func() error {
		lib, err := db.lockedOne("SELECT id FROM libraries WHERE id=?", req.Library)
		if err != nil {
			return err
		}
		if lib == nil {
			return errors.New("shared library not found")
		}
		if req.OriginKind == "object" {
			original, err := db.lockedOne(`SELECT sha256 FROM library_objects
				WHERE library=? AND name=?`, req.Library, req.OriginName)
			if err != nil {
				return err
			}
			if original == nil {
				return errors.New("source object not found in shared library")
			}
			digest = asString(original["sha256"])
			if !shaPattern.MatchString(digest) {
				return errors.New("stored source checksum is invalid")
			}
		} else {
			original, err := db.lockedOne(`SELECT revision FROM library_sources
				WHERE library=? AND repository=? AND path=?`,
				req.Library, req.OriginName, req.OriginPath)
			if err != nil {
				return err
			}
			if original == nil || asString(original["revision"]) != req.OriginRevision {
				return errors.New("Git source must match the library's exact pinned revision")
			}
			// A commit identifies Git source history; it is not a content
			// SHA-256 for the selected document.
			digest = ""
		}
		return db.execLocked(`INSERT INTO research_sources
			(id,library,title,origin_kind,origin_name,origin_revision,origin_digest,
			 origin_path,epoch,index_revision,created,updated)
			VALUES(?,?,?,?,?,?,?,?,1,0,?,?)`,
			id, req.Library, req.Title, req.OriginKind, req.OriginName,
			req.OriginRevision, digest, req.OriginPath, now, now)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "library": req.Library, "title": req.Title,
		"origin_kind": req.OriginKind, "origin_name": req.OriginName,
		"origin_revision": req.OriginRevision, "origin_digest": digest,
		"origin_path": req.OriginPath, "index_revision": 0}, nil
}

func validatePassage(p ResearchPassageInput) error {
	if p.Ordinal < 1 || p.Ordinal > 100000000 || p.Page < 0 || p.Page > 10000000 ||
		len(p.Section) > 160 || len(p.Text) < 1 || len(p.Text) > 8192 ||
		!utf8.ValidString(p.Text) || strings.ContainsRune(p.Text, 0) ||
		p.LineStart < 0 || p.LineEnd < 0 || p.LineStart > 10000000 ||
		p.LineEnd > 10000000 ||
		(p.LineStart == 0) != (p.LineEnd == 0) ||
		(p.LineStart > 0 && p.LineEnd < p.LineStart) || len(p.Topics) > 12 {
		return errors.New("invalid research passage")
	}
	seen := map[string]bool{}
	for _, topic := range p.Topics {
		if !researchTopic.MatchString(topic) || seen[topic] {
			return errors.New("invalid or duplicate research topic")
		}
		seen[topic] = true
	}
	return nil
}

// IngestResearchPassages commits one bounded batch into the active index
// epoch. Replacing creates a new epoch without deleting prior evidence.
func (db *Database) IngestResearchPassages(library, source, mode string, expectedRevision int, passages []ResearchPassageInput) (map[string]any, error) {
	if !validID.MatchString(library) || !contributionArtifactID.MatchString(source) ||
		(mode != "append" && mode != "replace") || expectedRevision < 0 ||
		len(passages) < 1 || len(passages) > 64 {
		return nil, errors.New("invalid passage batch")
	}
	seen := map[int]bool{}
	for _, p := range passages {
		if err := validatePassage(p); err != nil {
			return nil, err
		}
		if seen[p.Ordinal] {
			return nil, errors.New("duplicate passage ordinal in batch")
		}
		seen[p.Ordinal] = true
	}
	var result map[string]any
	err := db.transaction(func() error {
		original, err := db.lockedOne(`SELECT epoch,index_revision FROM research_sources
			WHERE id=? AND library=?`, source, library)
		if err != nil {
			return err
		}
		if original == nil {
			return errors.New("research source not found")
		}
		if asInt(original["index_revision"]) != expectedRevision {
			return errors.New("research index revision conflict")
		}
		epoch := asInt(original["epoch"])
		if mode == "replace" && expectedRevision > 0 {
			epoch++
		}
		revision := expectedRevision + 1
		now := stamp()
		for _, input := range passages {
			id := randomHex(16)
			topics, err := json.Marshal(input.Topics)
			if err != nil {
				return err
			}
			if err := db.execLocked(`INSERT INTO research_passages
				(id,source,epoch,batch_revision,ordinal,page,section,line_start,
				 line_end,body,body_sha256,topics,created)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, source, epoch, revision, input.Ordinal, input.Page, input.Section,
				input.LineStart, input.LineEnd, input.Text, checksum([]byte(input.Text)),
				string(topics), now); err != nil {
				return err
			}
			row, err := db.lockedOne("SELECT rowid AS row_id FROM research_passages WHERE id=?", id)
			if err != nil || row == nil {
				return errors.New("cannot identify indexed passage")
			}
			if err := db.execLocked("INSERT INTO research_passages_fts(rowid,body) VALUES(?,?)",
				int64(asFloat(row["row_id"])), input.Text); err != nil {
				return err
			}
		}
		if err := db.execLocked(`UPDATE research_sources SET epoch=?,index_revision=?,updated=?
			WHERE id=? AND library=?`, epoch, revision, now, source, library); err != nil {
			return err
		}
		result = map[string]any{"library": library, "source_id": source,
			"epoch": epoch, "index_revision": revision,
			"mode": mode, "indexed_count": len(passages)}
		return nil
	})
	return result, err
}

func (db *Database) ResearchSources(project string) ([]map[string]any, error) {
	if !validID.MatchString(project) {
		return nil, errors.New("invalid project scope")
	}
	return db.query(`SELECT s.id,s.library,s.title,s.origin_kind,s.origin_name,
		s.origin_revision,s.origin_digest,s.origin_path,s.epoch,s.index_revision,
		s.created,s.updated FROM research_sources s
		JOIN project_libraries l ON l.library=s.library WHERE l.project=?
		ORDER BY s.title,s.id LIMIT 500`, project)
}

func termsForResearch(query string) (string, error) {
	if strings.TrimSpace(query) == "" || len(query) > 512 {
		return "", errors.New("invalid research search query")
	}
	terms := researchTerms.FindAllString(strings.ToLower(query), -1)
	if len(terms) == 0 {
		return "", errors.New("research search has no indexable terms")
	}
	if len(terms) > 12 {
		terms = terms[:12]
	}
	clauses := make([]string, 0, len(terms))
	for _, word := range terms {
		// RE2 strips all FTS5 query operators, so the user controls only
		// term values rather than SQLite MATCH syntax.
		clauses = append(clauses, "\""+word+"\"")
	}
	return strings.Join(clauses, " AND "), nil
}

func (db *Database) SearchResearch(project, query string, limit int) ([]map[string]any, error) {
	if !validID.MatchString(project) || limit < 1 || limit > 100 {
		return nil, errors.New("invalid research search scope or limit")
	}
	fts, err := termsForResearch(query)
	if err != nil {
		return nil, err
	}
	rows, err := db.query(`SELECT p.id,p.source,s.library,s.title,
		s.origin_kind,s.origin_name,s.origin_revision,s.origin_digest,s.origin_path,
		p.epoch,p.ordinal,p.page,p.section,p.line_start,p.line_end,
		p.body_sha256,p.topics,substr(p.body,1,1200) excerpt
		FROM research_passages_fts f
		JOIN research_passages p ON p.rowid=f.rowid
		JOIN research_sources s ON s.id=p.source AND s.epoch=p.epoch
		JOIN project_libraries l ON l.library=s.library
		WHERE l.project=? AND f.body MATCH ?
		ORDER BY p.created DESC,p.ordinal LIMIT ?`, project, fts, limit)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["citation_ref"] = fmt.Sprintf("source:%s@%d:%d",
			asString(row["source"]), asInt(row["epoch"]), asInt(row["ordinal"]))
		var topics []string
		if err := json.Unmarshal([]byte(asString(row["topics"])), &topics); err != nil {
			return nil, err
		}
		row["topics"] = topics
	}
	return rows, nil
}

func (db *Database) ReadResearchPassage(project, id string) (map[string]any, error) {
	if !validID.MatchString(project) || !contributionArtifactID.MatchString(id) {
		return nil, errors.New("invalid research passage")
	}
	row, err := db.queryOne(`SELECT p.id,p.source,s.library,s.title,
		s.origin_kind,s.origin_name,s.origin_revision,s.origin_digest,s.origin_path,
		p.epoch,p.batch_revision,p.ordinal,p.page,p.section,p.line_start,p.line_end,
		p.body,p.body_sha256,p.topics,p.created
		FROM research_passages p JOIN research_sources s ON s.id=p.source
		JOIN project_libraries l ON l.library=s.library WHERE p.id=? AND l.project=?`,
		id, project)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("research passage unavailable to project")
	}
	if checksum([]byte(asString(row["body"]))) != asString(row["body_sha256"]) {
		return nil, errors.New("stored research passage checksum mismatch")
	}
	var topics []string
	if err := json.Unmarshal([]byte(asString(row["topics"])), &topics); err != nil {
		return nil, err
	}
	row["topics"] = topics
	row["citation_ref"] = fmt.Sprintf("source:%s@%d:%d",
		asString(row["source"]), asInt(row["epoch"]), asInt(row["ordinal"]))
	return row, nil
}
