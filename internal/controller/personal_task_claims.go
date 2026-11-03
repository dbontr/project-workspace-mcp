package controller

import (
	"encoding/json"
	"errors"
)

// Personal task claims use the same fenced, expiring lease contract as project
// task claims but are scoped to the authenticated owner and personal task ID.
func (db *Database) PersonalTaskClaimList(owner, task string) ([]map[string]any, error) {
	if _, err := db.PersonalTaskInfo(owner, task); err != nil {
		return nil, err
	}
	return db.query(`SELECT task,work_key,generation,status,expires,results,created,updated
		FROM personal_task_claims WHERE task=? ORDER BY updated DESC,work_key LIMIT 1000`, task)
}

func (db *Database) PersonalTaskClaimStatus(owner, task, key string) (map[string]any, error) {
	if !validWorkKey.MatchString(key) {
		return nil, errors.New("invalid work key")
	}
	if _, err := db.PersonalTaskInfo(owner, task); err != nil {
		return nil, err
	}
	row, err := db.queryOne(`SELECT task,work_key,generation,status,expires,results,created,updated
		FROM personal_task_claims WHERE task=? AND work_key=?`, task, key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("personal task claim not found")
	}
	if asString(row["status"]) == "claimed" && asFloat(row["expires"]) <= stamp() {
		row["status"] = "expired"
	}
	var results []string
	if err := json.Unmarshal([]byte(asString(row["results"])), &results); err != nil {
		return nil, errors.New("invalid stored personal task claim results")
	}
	row["results"] = results
	return row, nil
}

func (db *Database) personalTaskOwnerLocked(owner, task string, requireActive bool) error {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) {
		return errors.New("invalid personal task owner or ID")
	}
	row, err := db.lockedOne("SELECT status FROM personal_tasks WHERE id=? AND owner=?", task, owner)
	if err != nil {
		return err
	}
	if row == nil || requireActive && asString(row["status"]) != "active" {
		return errors.New("personal task not found or inactive")
	}
	return nil
}

func (db *Database) ClaimPersonalTask(owner, task, key string, leaseSeconds int) (map[string]any, error) {
	if !validWorkKey.MatchString(key) || !validClaimLease(leaseSeconds) {
		return nil, errors.New("invalid personal work key or duration")
	}
	token, now := randomHex(32), stamp()
	var result map[string]any
	err := db.transaction(func() error {
		if err := db.personalTaskOwnerLocked(owner, task, true); err != nil {
			return err
		}
		row, err := db.lockedOne(`SELECT generation,status,expires,results
			FROM personal_task_claims WHERE task=? AND work_key=?`, task, key)
		if err != nil {
			return err
		}
		if row != nil && asString(row["status"]) == "completed" {
			var results []string
			if err := json.Unmarshal([]byte(asString(row["results"])), &results); err != nil {
				return err
			}
			result = map[string]any{"task_id": task, "work_key": key, "status": "completed",
				"generation": row["generation"], "results": results}
			return nil
		}
		if row != nil && asString(row["status"]) == "claimed" && asFloat(row["expires"]) > now {
			result = map[string]any{"task_id": task, "work_key": key, "status": "busy",
				"generation": row["generation"], "expires": row["expires"]}
			return nil
		}
		generation := 1
		if row != nil {
			generation = asInt(row["generation"]) + 1
		}
		expiry := now + float64(leaseSeconds)
		if err := db.execLocked(`INSERT INTO personal_task_claims
			(task,work_key,generation,token_hash,status,expires,results,created,updated)
			VALUES(?,?,?,?,'claimed',?,'[]',?,?)
			ON CONFLICT(task,work_key) DO UPDATE SET
			generation=excluded.generation,token_hash=excluded.token_hash,status='claimed',
			expires=excluded.expires,results='[]',updated=excluded.updated`,
			task, key, generation, hashToken(token), expiry, now, now); err != nil {
			return err
		}
		result = map[string]any{"task_id": task, "work_key": key, "status": "claimed",
			"generation": generation, "expires": expiry, "lease_token": token}
		return nil
	})
	return result, err
}

func (db *Database) personalCurrentClaimLocked(owner, task, key, token string, generation int) error {
	if !validWorkKey.MatchString(key) || generation < 1 || len(token) != 64 {
		return errors.New("invalid personal claim credentials")
	}
	if err := db.personalTaskOwnerLocked(owner, task, false); err != nil {
		return err
	}
	row, err := db.lockedOne(`SELECT generation,token_hash,status,expires
		FROM personal_task_claims WHERE task=? AND work_key=?`, task, key)
	if err != nil {
		return err
	}
	if row == nil || asInt(row["generation"]) != generation ||
		!equalSecret(asString(row["token_hash"]), hashToken(token)) ||
		asString(row["status"]) != "claimed" || asFloat(row["expires"]) <= stamp() {
		return errors.New("stale or expired personal task lease")
	}
	return nil
}

func (db *Database) RenewPersonalTaskClaim(owner, task, key, token string, generation, leaseSeconds int) (map[string]any, error) {
	if !validClaimLease(leaseSeconds) {
		return nil, errors.New("invalid lease duration")
	}
	var result map[string]any
	err := db.transaction(func() error {
		if err := db.personalCurrentClaimLocked(owner, task, key, token, generation); err != nil {
			return err
		}
		now, expires := stamp(), stamp()+float64(leaseSeconds)
		if err := db.execLocked(`UPDATE personal_task_claims
			SET expires=?,updated=? WHERE task=? AND work_key=?`,
			expires, now, task, key); err != nil {
			return err
		}
		result = map[string]any{"task_id": task, "work_key": key,
			"status": "claimed", "generation": generation, "expires": expires}
		return nil
	})
	return result, err
}

func (db *Database) FinishPersonalTaskClaim(owner, task, key, token, status string, generation int, results []string) (map[string]any, error) {
	if status != "completed" && status != "released" {
		return nil, errors.New("invalid final claim status")
	}
	if len(results) > 128 || (status == "released" && len(results) != 0) {
		return nil, errors.New("invalid result artifact list")
	}
	var out map[string]any
	err := db.transaction(func() error {
		if err := db.personalCurrentClaimLocked(owner, task, key, token, generation); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range results {
			if !contributionArtifactID.MatchString(id) || seen[id] {
				return errors.New("invalid or duplicate task file artifact")
			}
			seen[id] = true
			row, err := db.lockedOne(`SELECT artifact_id FROM personal_task_files
				WHERE task=? AND artifact_id=?`, task, id)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("claim result is not an immutable file from this task")
			}
		}
		raw, err := json.Marshal(results)
		if err != nil {
			return err
		}
		if err := db.execLocked(`UPDATE personal_task_claims SET token_hash=NULL,
			status=?,expires=0,results=?,updated=? WHERE task=? AND work_key=?`,
			status, string(raw), stamp(), task, key); err != nil {
			return err
		}
		out = map[string]any{"task_id": task, "work_key": key, "status": status,
			"generation": generation, "results": results}
		return nil
	})
	return out, err
}
