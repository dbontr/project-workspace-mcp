package controller

import (
	"errors"
	"strings"
)

const personalJobRetentionSeconds = 24 * 60 * 60

// SubmitPersonalTaskJob queues an isolated one-shot worker command without a
// project or project knowledge destination. Only approved worker images run.
func (db *Database) SubmitPersonalTaskJob(owner, task, command, image string, cpu float64, memory int) (map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) ||
		strings.TrimSpace(command) == "" || len(command) > 16000 ||
		!validImage.MatchString(image) || cpu < 0.1 || cpu > 32 ||
		memory < 64 || memory > 131072 {
		return nil, errors.New("invalid personal task job")
	}
	id := randomHex(16)
	var result map[string]any
	err := db.transaction(func() error {
		row, err := db.lockedOne("SELECT id FROM personal_tasks WHERE id=? AND owner=? AND status='active'", task, owner)
		if err != nil || row == nil {
			return errors.New("active personal task owned by caller required")
		}
		now := stamp()
		if err := db.execLocked(`INSERT INTO jobs
			(id,project,command,image,cpu,memory_mb,status,created,updated)
			VALUES(?,'',?,?,?,?,'queued',?,?)`,
			id, command, image, cpu, memory, now, now); err != nil {
			return err
		}
		if err := db.execLocked("INSERT INTO personal_task_jobs VALUES(?,?,?,?)",
			task, id, owner, now); err != nil {
			return err
		}
		result = map[string]any{"job_id": id, "task_id": task, "status": "queued",
			"scope": "task", "knowledge_persisted": false,
			"retention_seconds": personalJobRetentionSeconds}
		return nil
	})
	return result, err
}

func (db *Database) expirePersonalJobResults() error {
	return db.transaction(func() error {
		cutoff := stamp() - personalJobRetentionSeconds
		// Do not delete active work. Finished output is operational state and
		// expires independently of project artifacts and knowledge.
		if err := db.execLocked(`DELETE FROM personal_task_jobs WHERE job IN (
			SELECT j.id FROM jobs j WHERE j.status IN ('succeeded','failed','expired')
			AND j.updated < ? AND j.project='')`, cutoff); err != nil {
			return err
		}
		return db.execLocked(`DELETE FROM jobs WHERE project='' AND
			status IN ('succeeded','failed','expired') AND updated < ? AND
			NOT EXISTS (SELECT 1 FROM personal_task_jobs t WHERE t.job=jobs.id)`, cutoff)
	})
}

func (db *Database) PersonalTaskJobs(owner, task string) ([]map[string]any, error) {
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) {
		return nil, errors.New("invalid personal task")
	}
	row, err := db.PersonalTaskInfo(owner, task)
	if err != nil || row == nil {
		return nil, errors.New("personal task not found")
	}
	if err := db.expirePersonalJobResults(); err != nil {
		return nil, err
	}
	return db.query(`SELECT j.id,j.status,j.node,j.image,j.cpu,j.memory_mb,
		j.result,j.created,j.updated FROM personal_task_jobs t
		JOIN jobs j ON j.id=t.job WHERE t.task=? AND t.owner=?
		ORDER BY j.created DESC LIMIT 100`, task, owner)
}
