package controller

import (
	"errors"
	"fmt"
)

// Job device leases and workspace hardware leases share one SQLite writer.
// A device is eligible only when neither subsystem currently owns it.
func (db *Database) jobHardwareSelectionLocked(job, node string, capabilities map[string]any) ([]map[string]any, bool, error) {
	row, err := db.lockedOne("SELECT requirements,min_gpu_memory_mb FROM personal_task_job_hardware WHERE job=?", job)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, true, nil
	}
	request, err := validHardwareRequest(decodeObject(asString(row["requirements"])))
	if err != nil {
		return nil, false, err
	}
	busy := map[string]bool{}
	for _, statement := range []string{
		"SELECT device_id FROM hardware_leases WHERE node=? AND released IS NULL",
		"SELECT device_id FROM task_job_device_leases WHERE node=? AND released IS NULL",
	} {
		leases, err := db.queryLocked(statement, node)
		if err != nil {
			return nil, false, err
		}
		for _, lease := range leases {
			busy[asString(lease["device_id"])] = true
		}
	}
	candidates := devicesIn(capabilities)
	selected := []map[string]any{}
	minGPU := asInt(row["min_gpu_memory_mb"])
	for _, kind := range []string{"fpga", "gpu", "npu"} {
		needed := request[kind]
		for _, device := range candidates {
			id := asString(device["id"])
			if needed == 0 {
				break
			}
			if busy[id] || asString(device["kind"]) != kind {
				continue
			}
			if kind == "gpu" && asFloat(device["memory_mb"]) < float64(minGPU) {
				continue
			}
			busy[id] = true
			selected = append(selected, map[string]any{"id": id, "kind": kind})
			needed--
		}
		if needed > 0 {
			return nil, false, nil
		}
	}
	return selected, true, nil
}

func (db *Database) leaseJobHardwareLocked(job, node string, selected []map[string]any) error {
	for _, device := range selected {
		if err := db.execLocked(`INSERT INTO task_job_device_leases
			(job,node,device_id,kind,acquired,released)
			VALUES(?,?,?,?,?,NULL)`, job, node, device["id"], device["kind"], stamp()); err != nil {
			return err
		}
	}
	return nil
}

// A completed job has already acknowledged its lease and reported a result.
// Expired jobs retain device reservations until an operator verifies cleanup.
func (db *Database) releaseJobHardwareLocked(job, node string) error {
	return db.execLocked("UPDATE task_job_device_leases SET released=? WHERE job=? AND node=? AND released IS NULL",
		stamp(), job, node)
}

func (db *Database) RecoverTaskHardware(job string, confirm, cleanupVerified bool) (map[string]any, error) {
	if !confirm || !cleanupVerified || !contributionArtifactID.MatchString(job) {
		return nil, errors.New("releasing an uncertain hardware lease requires explicit cleanup verification and confirmation")
	}
	var count int
	err := db.transaction(func() error {
		record, err := db.lockedOne("SELECT node,status FROM jobs WHERE id=?", job)
		if err != nil {
			return err
		}
		if record == nil || (asString(record["status"]) != "expired" &&
			asString(record["status"]) != "failed" &&
			asString(record["status"]) != "succeeded") {
			return errors.New("hardware recovery requires a terminal job")
		}
		row, err := db.lockedOne("SELECT count(*) n FROM task_job_device_leases WHERE job=? AND released IS NULL", job)
		if err != nil {
			return err
		}
		count = asInt(row["n"])
		if count == 0 {
			return errors.New("no unresolved job hardware lease found")
		}
		if err := db.releaseJobHardwareLocked(job, asString(record["node"])); err != nil {
			return fmt.Errorf("job hardware recovery failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"job_id": job, "released_devices": count,
		"cleanup_verified": true}, nil
}
