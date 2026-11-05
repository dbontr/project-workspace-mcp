package controller

import (
	"errors"
	"strings"
)

// A personal compute job has no project row, repository link or knowledge
// destination. Kubernetes needs an operator-managed worker whose project_id
// matches this execution-only scope; Docker compatibility workers can also
// claim these jobs under their existing capacity policy.
const personalComputeScope = "personal-tasks"

func (db *Database) SubmitPersonalTaskJob(owner, task, command, image string, cpu float64, memory int) (map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) ||
		strings.TrimSpace(command) == "" || len(command) > 16000 ||
		!validImage.MatchString(image) || cpu < 0.1 || cpu > 32 ||
		memory < 64 || memory > 131072 {
		return nil, errors.New("invalid independent compute job")
	}
	var result map[string]any
	err := db.transaction(func() error {
		t, err := db.lockedOne("SELECT id FROM personal_tasks WHERE id=? AND owner=? AND status='active'", task, owner)
		if err != nil || t == nil {
			return errors.New("active task owned by caller required")
		}
		busy, err := db.lockedOne(`SELECT count(*) n FROM personal_task_jobs x
			JOIN jobs j ON j.id=x.job
			WHERE x.task=? AND j.status IN ('queued','leased')`, task)
		if err != nil {
			return err
		}
		if asInt(busy["n"]) >= 8 {
			return errors.New("independent task compute queue is full")
		}
		nodes, err := db.queryLocked("SELECT capabilities,last_seen FROM nodes WHERE last_seen>?", stamp()-90)
		if err != nil {
			return err
		}
		eligible := false
		for _, node := range nodes {
			caps := decodeObject(asString(node["capabilities"]))
			if (asBool(caps["docker"]) || asBool(caps["kubernetes"]) &&
				asString(caps["project_id"]) == personalComputeScope) &&
				asFloat(caps["cpu"]) >= cpu && asFloat(caps["memory_mb"]) >= float64(memory) {
				eligible = true
				break
			}
		}
		if !eligible {
			return errors.New("no approved standalone task worker is online")
		}
		id, now := randomHex(16), stamp()
		if err := db.execLocked(`INSERT INTO jobs
			(id,project,command,image,cpu,memory_mb,status,created,updated)
			VALUES(?,?,?,?,?,?,'queued',?,?)`,
			id, personalComputeScope, command, image, cpu, memory, now, now); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO personal_task_jobs(task,job,owner,created) VALUES(?,?,?,?)",
			task, id, owner, now); err != nil {
			return err
		}
		result = map[string]any{"id": id, "task_id": task, "status": "queued",
			"storage": "task-operational", "project_knowledge": false}
		return nil
	})
	return result, err
}

func (db *Database) PersonalTaskJobs(owner, task string) ([]map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) {
		return nil, errors.New("invalid personal task")
	}
	found, err := db.queryOne("SELECT id FROM personal_tasks WHERE id=? AND owner=?", task, owner)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, errors.New("personal task not found")
	}
	return db.query(`SELECT j.id,j.status,j.node,j.created,j.updated,j.result
		FROM personal_task_jobs x JOIN jobs j ON j.id=x.job
		WHERE x.task=? AND x.owner=? ORDER BY j.created DESC LIMIT 100`,
		task, owner)
}
