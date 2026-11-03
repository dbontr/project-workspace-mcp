package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var validWorkKey = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)

func validClaimLease(seconds int) bool { return seconds >= 60 && seconds <= 3600 }

func (db *Database) ListTaskClaims(project string) ([]map[string]any, error) {
	if err := requireID(project); err != nil {
		return nil, err
	}
	return db.query(`SELECT project,work_key,generation,status,expires,results,created,updated FROM task_claims WHERE project=? ORDER BY updated DESC LIMIT 1000`, project)
}

// ClaimTask reserves one stable project and work-key pair. A previously
// completed key returns its durable result instead of repeating the work.
// The random token is returned once and never appears in a listing.
func (db *Database) ClaimTask(project, key string, leaseSeconds int) (map[string]any, error) {
	if !validWorkKey.MatchString(key) || !validClaimLease(leaseSeconds) {
		return nil, errors.New("invalid work key or lease duration")
	}
	token := randomHex(32)
	now := stamp()
	out := map[string]any{}
	err := db.transaction(func() error {
		if err := requireProject(db, project); err != nil {
			return err
		}
		row, err := db.lockedOne("SELECT generation,status,expires,results FROM task_claims WHERE project=? AND work_key=?", project, key)
		if err != nil {
			return err
		}
		if row != nil && asString(row["status"]) == "completed" {
			var results []string
			if err := json.Unmarshal([]byte(asString(row["results"])), &results); err != nil {
				return err
			}
			out = map[string]any{"project": project, "work_key": key, "status": "completed", "generation": row["generation"], "results": results}
			return nil
		}
		if row != nil && asString(row["status"]) == "claimed" && asFloat(row["expires"]) > now {
			out = map[string]any{"project": project, "work_key": key, "status": "busy", "generation": row["generation"], "expires": row["expires"]}
			return nil
		}
		generation := 1
		if row != nil {
			generation = asInt(row["generation"]) + 1
		}
		until := now + float64(leaseSeconds)
		if err := db.execLocked(`INSERT INTO task_claims(project,work_key,generation,token_hash,status,expires,results,created,updated)
			VALUES(?,?,?,?,'claimed',?,'[]',?,?)
			ON CONFLICT(project,work_key) DO UPDATE SET
				generation=excluded.generation,token_hash=excluded.token_hash,status='claimed',
				expires=excluded.expires,results='[]',updated=excluded.updated`,
			project, key, generation, hashToken(token), until, now, now); err != nil {
			return err
		}
		out = map[string]any{"project": project, "work_key": key, "status": "claimed", "generation": generation, "expires": until, "lease_token": token}
		return nil
	})
	return out, err
}

func (db *Database) currentClaimLocked(project, key, token string, generation int) (map[string]any, error) {
	if !validWorkKey.MatchString(key) || generation <= 0 || len(token) != 64 {
		return nil, errors.New("invalid claim credential")
	}
	row, err := db.lockedOne("SELECT generation,token_hash,status,expires FROM task_claims WHERE project=? AND work_key=?", project, key)
	if err != nil {
		return nil, err
	}
	if row == nil || asInt(row["generation"]) != generation ||
		!equalSecret(asString(row["token_hash"]), hashToken(token)) ||
		asString(row["status"]) != "claimed" || asFloat(row["expires"]) <= stamp() {
		return nil, errors.New("stale or expired task lease")
	}
	return row, nil
}

func (db *Database) RenewTaskClaim(project, key, token string, generation, leaseSeconds int) (map[string]any, error) {
	if !validClaimLease(leaseSeconds) {
		return nil, errors.New("invalid lease duration")
	}
	var result map[string]any
	err := db.transaction(func() error {
		if _, err := db.currentClaimLocked(project, key, token, generation); err != nil {
			return err
		}
		until := stamp() + float64(leaseSeconds)
		if err := db.execLocked("UPDATE task_claims SET expires=?,updated=? WHERE project=? AND work_key=?", until, stamp(), project, key); err != nil {
			return err
		}
		result = map[string]any{"project": project, "work_key": key, "status": "claimed", "generation": generation, "expires": until}
		return nil
	})
	return result, err
}

// FinishTaskClaim checks every recorded artifact's project ownership before
// publishing result IDs; release does not mark a task completed.
func (db *Database) FinishTaskClaim(project, key, token, status string, generation int, artifacts []string) (map[string]any, error) {
	if status != "completed" && status != "released" {
		return nil, errors.New("invalid final claim status")
	}
	if len(artifacts) > 128 || (status == "released" && len(artifacts) > 0) {
		return nil, errors.New("invalid result artifact IDs")
	}
	var result map[string]any
	err := db.transaction(func() error {
		if _, err := db.currentClaimLocked(project, key, token, generation); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range artifacts {
			if len(id) != 32 || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || seen[id] {
				return errors.New("invalid or duplicate result artifact ID")
			}
			seen[id] = true
			row, err := db.lockedOne("SELECT id FROM artifacts WHERE id=? AND project=?", id, project)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("result artifact is not owned by project")
			}
		}
		encoded, err := json.Marshal(artifacts)
		if err != nil {
			return err
		}
		if err := db.execLocked("UPDATE task_claims SET token_hash=NULL,status=?,expires=0,results=?,updated=? WHERE project=? AND work_key=?", status, string(encoded), stamp(), project, key); err != nil {
			return err
		}
		result = map[string]any{"project": project, "work_key": key, "status": status, "generation": generation, "results": artifacts}
		return nil
	})
	return result, err
}

// TaskClaimStatus omits the bearer token and records durable completion
// across controller restarts. Expired claims become reclaimable.
func (db *Database) TaskClaimStatus(project, key string) (map[string]any, error) {
	if !validWorkKey.MatchString(key) {
		return nil, errors.New("invalid work key")
	}
	row, err := db.queryOne("SELECT project,work_key,generation,status,expires,results,created,updated FROM task_claims WHERE project=? AND work_key=?", project, key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("task claim not found")
	}
	if asString(row["status"]) == "claimed" && asFloat(row["expires"]) <= float64(time.Now().UnixNano())/1e9 {
		row["status"] = "expired"
	}
	var results []string
	if err := json.Unmarshal([]byte(asString(row["results"])), &results); err != nil {
		return nil, fmt.Errorf("corrupt task result records: %w", err)
	}
	row["results"] = results
	return row, nil
}
