package controller

import (
	"errors"
	"regexp"
	"strings"
)

type ContributionRequest struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	ArtifactID string `json:"artifact_id"`
}

var contributionArtifactID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validContributionKind(kind string) bool {
	switch kind {
	case "handoff", "decision", "finding", "note":
		return true
	default:
		return false
	}
}

// ContributeProject stores an immutable project-scoped contribution and its
// exact source revision. A contribution never replaces canonical project notes.
func (db *Database) ContributeProject(project string, p ContributionRequest, principal string) (map[string]any, error) {
	if !validID.MatchString(project) || !validContributionKind(p.Kind) ||
		strings.TrimSpace(p.Title) == "" || len(p.Title) > 160 ||
		strings.TrimSpace(p.Body) == "" || len(p.Body) > 128<<10 ||
		(principal != "controller-owner" && !validPrincipal(principal)) {
		return nil, errors.New("invalid project contribution")
	}
	if (p.Repository == "") != (p.Revision == "") {
		return nil, errors.New("repository provenance requires an exact revision")
	}
	if p.Repository != "" && (!validSourceRepository(p.Repository) || !validCommitSHA.MatchString(p.Revision)) {
		return nil, errors.New("invalid immutable repository provenance")
	}
	if p.ArtifactID != "" && !contributionArtifactID.MatchString(p.ArtifactID) {
		return nil, errors.New("invalid source artifact ID")
	}
	id := randomHex(16)
	now := stamp()
	hash := checksum([]byte(p.Body))
	err := db.transaction(func() error {
		if err := requireProject(db, project); err != nil {
			return err
		}
		if p.Repository != "" {
			row, err := db.lockedOne("SELECT full_name FROM repositories WHERE project=? AND full_name=?", project, p.Repository)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("contribution source repository is not linked")
			}
		}
		if p.ArtifactID != "" {
			row, err := db.lockedOne("SELECT id FROM artifacts WHERE project=? AND id=?", project, p.ArtifactID)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("contribution source artifact does not belong to project")
			}
		}
		return db.execLocked(`INSERT INTO project_contributions
			(id,project,kind,title,body,body_sha256,repository,revision,artifact_id,principal,created)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, p.Kind, p.Title, p.Body, hash, p.Repository,
			p.Revision, p.ArtifactID, principal, now)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "project": project, "kind": p.Kind, "title": p.Title,
		"body_sha256": hash, "repository": p.Repository, "revision": p.Revision,
		"artifact_id": p.ArtifactID, "principal": principal, "created": now}, nil
}

func (db *Database) ProjectContribution(project, id string) (map[string]any, error) {
	if !validID.MatchString(project) || !contributionArtifactID.MatchString(id) {
		return nil, errors.New("invalid contribution reference")
	}
	row, err := db.queryOne("SELECT * FROM project_contributions WHERE project=? AND id=?", project, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("contribution not found")
	}
	if checksum([]byte(asString(row["body"]))) != asString(row["body_sha256"]) {
		return nil, errors.New("contribution body integrity check failed")
	}
	return row, nil
}

func (db *Database) ProjectContributions(project, kind string, limit int) ([]map[string]any, error) {
	if !validID.MatchString(project) || kind != "" && !validContributionKind(kind) ||
		limit < 1 || limit > 100 {
		return nil, errors.New("invalid contribution listing")
	}
	const fields = `id,project,kind,title,body_sha256,repository,revision,
		artifact_id,principal,created,substr(body,1,1200) excerpt`
	if kind == "" {
		return db.query("SELECT "+fields+" FROM project_contributions WHERE project=? ORDER BY created DESC,id DESC LIMIT ?", project, limit)
	}
	return db.query("SELECT "+fields+" FROM project_contributions WHERE project=? AND kind=? ORDER BY created DESC,id DESC LIMIT ?", project, kind, limit)
}

func (db *Database) ProjectContributionSearch(project, query string) ([]map[string]any, error) {
	if !validID.MatchString(project) || len(query) > 512 || strings.TrimSpace(query) == "" {
		return nil, errors.New("invalid contribution search")
	}
	return db.query(`SELECT id,kind,title,repository,revision,body_sha256,created,
		substr(body,1,1200) excerpt FROM project_contributions
		WHERE project=? AND (title LIKE ? OR body LIKE ?)
		ORDER BY created DESC,id DESC LIMIT 50`,
		project, "%"+query+"%", "%"+query+"%")
}

// ProjectContext derives the canonical working context from the existing
// revision-guarded notes and the latest independent contributions.
func (db *Database) ProjectContext(project string) (map[string]any, error) {
	if !validID.MatchString(project) {
		return nil, errors.New("invalid project")
	}
	info, err := db.queryOne("SELECT id,title,created FROM projects WHERE id=?", project)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, errors.New("project not found")
	}
	notes, err := db.query(`SELECT name,body,version,updated FROM notes
		WHERE project=? AND name IN ('project','context') ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	var projectDoc, contextDoc any
	for _, note := range notes {
		if asString(note["name"]) == "project" {
			projectDoc = note
		}
		if asString(note["name"]) == "context" {
			contextDoc = note
		}
	}
	handOffs, err := db.query(`SELECT id,kind,title,body,body_sha256,repository,revision,
		artifact_id,principal,created FROM project_contributions
		WHERE project=? AND kind='handoff' ORDER BY created DESC,id DESC LIMIT 1`, project)
	if err != nil {
		return nil, err
	}
	var latestHandoff any
	if len(handOffs) != 0 {
		if checksum([]byte(asString(handOffs[0]["body"]))) != asString(handOffs[0]["body_sha256"]) {
			return nil, errors.New("latest handoff integrity check failed")
		}
		latestHandoff = handOffs[0]
	}
	decisions, err := db.ProjectContributions(project, "decision", 20)
	if err != nil {
		return nil, err
	}
	repositories, err := db.RepositoryMemberships(project)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project": info, "authority": "controller-local",
		"project_document": projectDoc, "context_document": contextDoc,
		"latest_handoff": latestHandoff, "recent_decisions": decisions,
		"repositories": repositories,
		"note_keys":    map[string]string{"Project.md": "project", "Context.md": "context"},
	}, nil
}
