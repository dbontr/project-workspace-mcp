package controller

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type ProgramProposal struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	Distribution string `json:"distribution"`
	SourceURL    string `json:"source_url"`
	LicenseNotes string `json:"license_notes"`
}

func poolEntryTable(kind string) (string, error) {
	switch kind {
	case "image":
		return "pool_images", nil
	case "software":
		return "pool_software", nil
	default:
		return "", errors.New("invalid pool entry type")
	}
}

func validPoolStatus(status string) bool {
	return status == "active" || status == "disabled" || status == "retired"
}

// Missing state rows represent approved entries created before lifecycle
// management was introduced. Retired entries cannot be reactivated.
func (db *Database) poolStateLocked(kind, id string) (string, int, error) {
	row, err := db.lockedOne("SELECT status,revision FROM pool_entry_states WHERE kind=? AND entry_id=?", kind, id)
	if err != nil {
		return "", 0, err
	}
	if row == nil {
		return "active", 1, nil
	}
	return asString(row["status"]), asInt(row["revision"]), nil
}

func (db *Database) poolActive(kind, id string) (bool, error) {
	row, err := db.queryOne("SELECT status FROM pool_entry_states WHERE kind=? AND entry_id=?", kind, id)
	if err != nil {
		return false, err
	}
	return row == nil || asString(row["status"]) == "active", nil
}

// SetPoolState removes catalog entries from new allocations and environment
// compositions without modifying running containers or retained snapshots.
func (db *Database) SetPoolState(kind, id, status string, expectedRevision int, reason string) (map[string]any, error) {
	table, err := poolEntryTable(kind)
	if err != nil || !validID.MatchString(id) || !validPoolStatus(status) ||
		expectedRevision < 1 || len(reason) > 512 || strings.TrimSpace(reason) == "" {
		return nil, errors.New("invalid catalog transition")
	}
	var result map[string]any
	err = db.transaction(func() error {
		found, err := db.lockedOne("SELECT id FROM "+table+" WHERE id=?", id)
		if err != nil {
			return err
		}
		if found == nil {
			return errors.New("catalog entry not found")
		}
		previous, version, err := db.poolStateLocked(kind, id)
		if err != nil {
			return err
		}
		if version != expectedRevision || previous == "retired" || previous == status {
			return errors.New("catalog revision or transition conflict")
		}
		if kind == "software" && status == "active" {
			base, err := db.lockedOne("SELECT image_id FROM pool_software WHERE id=?", id)
			if err != nil {
				return err
			}
			baseStatus, _, err := db.poolStateLocked("image", asString(base["image_id"]))
			if err != nil || baseStatus != "active" {
				return errors.New("software base image is not active")
			}
		}
		now := stamp()
		if err := db.execLocked(`INSERT INTO pool_entry_states(kind,entry_id,status,revision,reason,updated)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(kind,entry_id) DO UPDATE SET status=excluded.status,
			revision=excluded.revision,reason=excluded.reason,updated=excluded.updated`,
			kind, id, status, version+1, reason, now); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO pool_events(kind,entry_id,revision,previous,status,reason,created) VALUES(?,?,?,?,?,?,?)",
			kind, id, version+1, previous, status, reason, now); err != nil {
			return err
		}
		result = map[string]any{"kind": kind, "id": id, "status": status, "revision": version + 1, "previous": previous, "reason": reason}
		return nil
	})
	return result, err
}

func (db *Database) PoolEvents(kind, id string) ([]map[string]any, error) {
	if kind != "program" && kind != "image" && kind != "software" {
		return nil, errors.New("invalid catalog event type")
	}
	if id != "" && !validID.MatchString(id) {
		return nil, errors.New("invalid catalog entry ID")
	}
	if id != "" {
		return db.query("SELECT kind,entry_id,revision,previous,status,reason,created FROM pool_events WHERE kind=? AND entry_id=? ORDER BY created DESC LIMIT 200", kind, id)
	}
	return db.query("SELECT kind,entry_id,revision,previous,status,reason,created FROM pool_events WHERE kind=? ORDER BY created DESC LIMIT 200", kind)
}

func validateProgram(p ProgramProposal) error {
	if !validID.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 ||
		len(p.Description) > 1024 || len(p.Version) > 64 || len(p.LicenseNotes) > 1024 {
		return errors.New("invalid program metadata")
	}
	switch p.Platform {
	case "linux-container":
		if p.Distribution != "apt" && p.Distribution != "oci" {
			return errors.New("Linux container software requires apt or OCI distribution")
		}
	case "windows-native", "macos-native":
		if p.Distribution != "native" {
			return errors.New("native applications require a native execution adapter")
		}
	default:
		return errors.New("unsupported program platform")
	}
	origin, err := url.Parse(p.SourceURL)
	if err != nil || len(p.SourceURL) > 512 || origin.Scheme != "https" ||
		origin.Hostname() == "" || origin.User != nil || origin.Fragment != "" {
		return errors.New("program source must be an HTTPS vendor or publisher page")
	}
	return nil
}

// Proposals record external software without downloading or executing it.
func (db *Database) ProposeProgram(p ProgramProposal) (map[string]any, error) {
	if err := validateProgram(p); err != nil {
		return nil, err
	}
	now := stamp()
	err := db.transaction(func() error {
		if err := db.execLocked(`INSERT INTO pool_programs
			(id,name,description,version,platform,distribution,source_url,license_notes,status,revision,pool_kind,pool_id,review_note,created,updated)
			VALUES(?,?,?,?,?,?,?,?,'requested',1,'','','',?,?)`,
			p.ID, p.Name, p.Description, p.Version, p.Platform, p.Distribution,
			p.SourceURL, p.LicenseNotes, now, now); err != nil {
			return err
		}
		return db.execLocked("INSERT INTO pool_events(kind,entry_id,revision,previous,status,reason,created) VALUES('program',?,1,'','requested','proposal',?)", p.ID, now)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "name": p.Name, "status": "requested", "revision": 1, "platform": p.Platform}, nil
}

func (db *Database) Programs() ([]map[string]any, error) {
	return db.query(`SELECT id,name,description,version,platform,distribution,source_url,license_notes,
		status,revision,pool_kind,pool_id,review_note,created,updated FROM pool_programs ORDER BY name,id`)
}

// ReviewProgram makes installation eligibility explicit. Native applications
// remain cataloged until an independently verified native adapter exists.
func (db *Database) ReviewProgram(id, status, reviewNote, poolKind, poolID string, expectedRevision int) (map[string]any, error) {
	if !validID.MatchString(id) || expectedRevision < 1 || len(reviewNote) > 512 ||
		strings.TrimSpace(reviewNote) == "" ||
		(status != "reviewed" && status != "available" && status != "disabled" && status != "rejected" && status != "retired") {
		return nil, errors.New("invalid program review")
	}
	var result map[string]any
	err := db.transaction(func() error {
		row, err := db.lockedOne("SELECT * FROM pool_programs WHERE id=?", id)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("program not found")
		}
		previous := asString(row["status"])
		if asInt(row["revision"]) != expectedRevision || previous == status ||
			previous == "retired" || previous == "rejected" {
			return errors.New("program revision or transition conflict")
		}
		switch status {
		case "reviewed":
			if previous != "requested" || strings.TrimSpace(asString(row["license_notes"])) == "" {
				return errors.New("program must have license notes before review")
			}
		case "available":
			if previous != "reviewed" && previous != "disabled" {
				return errors.New("program must be reviewed before activation")
			}
			if asString(row["platform"]) != "linux-container" {
				return errors.New("native software has no verified execution adapter")
			}
			expectedKind := "software"
			if asString(row["distribution"]) == "oci" {
				expectedKind = "image"
			}
			if poolKind != expectedKind || !validID.MatchString(poolID) {
				return errors.New("program requires the matching approved pool entry")
			}
			table, _ := poolEntryTable(poolKind)
			found, err := db.lockedOne("SELECT id FROM "+table+" WHERE id=?", poolID)
			if err != nil || found == nil {
				return errors.New("pool entry not found")
			}
			active, _, err := db.poolStateLocked(poolKind, poolID)
			if err != nil || active != "active" {
				return errors.New("linked pool entry is disabled")
			}
			if poolKind == "software" {
				base, err := db.lockedOne("SELECT image_id FROM pool_software WHERE id=?", poolID)
				if err != nil {
					return err
				}
				baseStatus, _, err := db.poolStateLocked("image", asString(base["image_id"]))
				if err != nil || baseStatus != "active" {
					return errors.New("linked software base image is disabled")
				}
			}
		case "disabled":
			if previous != "available" && previous != "reviewed" {
				return errors.New("only reviewed or available programs can be disabled")
			}
		}
		if status != "available" {
			poolKind, poolID = "", ""
		}
		now := stamp()
		if err := db.execLocked(`UPDATE pool_programs SET status=?,revision=?,pool_kind=?,pool_id=?,review_note=?,updated=?
			WHERE id=?`, status, expectedRevision+1, poolKind, poolID, reviewNote, now, id); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO pool_events(kind,entry_id,revision,previous,status,reason,created) VALUES('program',?,?,?,?,?,?)",
			id, expectedRevision+1, previous, status, reviewNote, now); err != nil {
			return err
		}
		result = map[string]any{"id": id, "status": status, "revision": expectedRevision + 1, "pool_kind": poolKind, "pool_id": poolID}
		return nil
	})
	return result, err
}

func (db *Database) PoolEntryInfo(kind, id string) (map[string]any, error) {
	table, err := poolEntryTable(kind)
	if err != nil || !validID.MatchString(id) {
		return nil, errors.New("invalid catalog reference")
	}
	row, err := db.queryOne("SELECT * FROM "+table+" WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%s %s not found", kind, id)
	}
	status, err := db.queryOne("SELECT status,revision,reason,updated FROM pool_entry_states WHERE kind=? AND entry_id=?", kind, id)
	if err != nil {
		return nil, err
	}
	if status == nil {
		row["status"], row["revision"] = "active", 1
	} else {
		for key, value := range status {
			row[key] = value
		}
	}
	return row, nil
}
