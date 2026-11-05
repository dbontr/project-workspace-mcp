package controller

import (
	"errors"
	"math"
	"strings"
)

// A personal compute job has no project row, repository link or knowledge
// destination. Every worker requires administrator approval for standalone
// tasks. Kubernetes workers also require the isolated personal-tasks scope.
const personalComputeScope = "personal-tasks"
const personalJobRetentionSeconds = 24 * 60 * 60

func (db *Database) SubmitPersonalTaskJob(owner, task, command, image string, cpu float64, memory int) (map[string]any, error) {
	return db.SubmitPersonalTaskJobWithHardware(owner, task, command, image, cpu, memory, nil, 0)
}

// Standalone jobs retain operational results but never create project records,
// project knowledge or Git commits.
func (db *Database) SubmitPersonalTaskJobWithHardware(owner, task, command, image string,
	cpu float64, memory int, hardware map[string]any, minGPUMemory int) (map[string]any, error) {
	request, err := validHardwareRequest(hardware)
	if err != nil {
		return nil, err
	}
	if request["usb"] != 0 || minGPUMemory < 0 || minGPUMemory > 1_000_000 ||
		(minGPUMemory > 0 && request["gpu"] == 0) {
		return nil, errors.New("standalone jobs support only approved GPU, NPU and FPGA devices")
	}
	if !validTaskOwner(owner) || !contributionArtifactID.MatchString(task) ||
		strings.TrimSpace(command) == "" || len(command) > 16000 ||
		!validImage.MatchString(image) || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0.1 || cpu > 32 ||
		memory < 64 || memory > 131072 {
		return nil, errors.New("invalid independent compute job")
	}
	var result map[string]any
	err = db.transaction(func() error {
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
		nodes, err := db.queryLocked(`SELECT n.capabilities,n.last_seen FROM nodes n
			JOIN personal_task_worker_access a ON a.node=n.id AND a.allowed=1
			WHERE n.last_seen>?`, stamp()-90)
		if err != nil {
			return err
		}
		eligible := false
		for _, node := range nodes {
			caps := decodeObject(asString(node["capabilities"]))
			scope := asString(caps["project_id"])
			scopeAllowed := asBool(caps["docker"]) && (scope == "" || scope == personalComputeScope) ||
				asBool(caps["kubernetes"]) && scope == personalComputeScope
			if !scopeAllowed || !workerHasImage(caps, image) ||
				asFloat(caps["cpu"]) < cpu || asFloat(caps["memory_mb"]) < float64(memory) {
				continue
			}
			counts := map[string]int{}
			for _, device := range devicesIn(caps) {
				if asString(device["kind"]) == "gpu" &&
					asFloat(device["memory_mb"]) < float64(minGPUMemory) {
					continue
				}
				counts[asString(device["kind"])]++
			}
			sufficient := true
			for kind, quantity := range request {
				if counts[kind] < quantity {
					sufficient = false
					break
				}
			}
			if sufficient {
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
		if len(request) != 0 {
			if err := db.execLocked("INSERT INTO personal_task_job_hardware(job,requirements,min_gpu_memory_mb) VALUES(?,?,?)",
				id, encode(request), minGPUMemory); err != nil {
				return err
			}
		}
		result = map[string]any{"id": id, "task_id": task, "status": "queued",
			"hardware": request, "storage": "task-operational", "project_knowledge": false}
		return nil
	})
	return result, err
}

// ExpirePersonalJobResults discards old operational logs without touching
// the task's explicitly saved files or any project's knowledge.
func (db *Database) ExpirePersonalJobResults() error {
	return db.transaction(func() error {
		cutoff := stamp() - personalJobRetentionSeconds
		if err := db.execLocked(`DELETE FROM personal_task_jobs WHERE job IN (
			SELECT id FROM jobs WHERE project=? AND status IN ('succeeded','failed','expired')
			AND updated<?) AND NOT EXISTS (
				SELECT 1 FROM task_job_device_leases l
				WHERE l.job=personal_task_jobs.job AND l.released IS NULL)`, personalComputeScope, cutoff); err != nil {
			return err
		}
		if err := db.execLocked(`DELETE FROM personal_task_job_hardware
			WHERE job IN (SELECT id FROM jobs WHERE project=?
			AND status IN ('succeeded','failed','expired') AND updated<?)
			AND NOT EXISTS (SELECT 1 FROM task_job_device_leases l
				WHERE l.job=personal_task_job_hardware.job AND l.released IS NULL)`, personalComputeScope, cutoff); err != nil {
			return err
		}
		if err := db.execLocked(`DELETE FROM task_job_device_leases WHERE job IN
			(SELECT id FROM jobs WHERE project=? AND status IN ('succeeded','failed','expired')
			AND updated<?) AND released IS NOT NULL`, personalComputeScope, cutoff); err != nil {
			return err
		}
		return db.execLocked(`DELETE FROM jobs WHERE project=?
			AND status IN ('succeeded','failed','expired')
			AND updated<? AND NOT EXISTS (
				SELECT 1 FROM personal_task_jobs t WHERE t.job=jobs.id)
			AND NOT EXISTS (
				SELECT 1 FROM task_job_device_leases l WHERE l.job=jobs.id AND l.released IS NULL)`,
			personalComputeScope, cutoff)
	})
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
	if err := db.ExpirePersonalJobResults(); err != nil {
		return nil, err
	}
	return db.query(`SELECT j.id,j.status,j.node,j.created,j.updated,j.result,
		coalesce(h.requirements,'{}') hardware,h.min_gpu_memory_mb
		FROM personal_task_jobs x JOIN jobs j ON j.id=x.job
		LEFT JOIN personal_task_job_hardware h ON h.job=j.id
		WHERE x.task=? AND x.owner=? ORDER BY j.created DESC LIMIT 100`,
		task, owner)
}
