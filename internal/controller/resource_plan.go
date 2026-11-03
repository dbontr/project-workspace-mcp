package controller

import (
	"errors"
	"math"
	"sort"
)

// PlanWorkspace inspects authoritative controller heartbeats and active lease
// records without reserving resources. CreateWorkspace must repeat admission.
func (db *Database) PlanWorkspace(req WorkspaceRequest) (map[string]any, error) {
	req.defaults()
	hardware, err := req.validatedHardware()
	if err != nil {
		return nil, err
	}
	type candidate struct {
		values map[string]any
		score  float64
	}
	candidates := []candidate{}
	blockers := []string{}
	var chosenImage string
	var recommended string
	err = db.transaction(func() error {
		if err := requireProject(db, req.ProjectID); err != nil {
			return err
		}
		image := req.Image
		arch, preferred := req.Architecture, ""
		if req.ImagePoolID != "" {
			pool, err := db.lockedOne("SELECT image,architecture FROM pool_images WHERE id=?", req.ImagePoolID)
			if err != nil {
				return err
			}
			if pool == nil {
				return errors.New("image pool entry not found")
			}
			image = asString(pool["image"])
			if target := asString(pool["architecture"]); target != "" {
				if arch != "" && arch != target {
					blockers = append(blockers, "requested architecture conflicts with image")
				}
				arch = target
			}
			status, _, err := db.poolStateLocked("image", req.ImagePoolID)
			if err != nil {
				return err
			}
			if status != "active" {
				blockers = append(blockers, "selected image pool entry is disabled or retired")
			}
		}
		if req.Environment != "" {
			env, err := db.lockedOne("SELECT image,architecture,node,status FROM environments WHERE project=? AND name=?", req.ProjectID, req.Environment)
			if err != nil {
				return err
			}
			if env == nil {
				return errors.New("environment is not defined")
			}
			if asString(env["status"]) != "ready" {
				blockers = append(blockers, "environment image is not ready")
			}
			image = asString(env["image"])
			if target := asString(env["architecture"]); target != "" {
				if arch != "" && arch != target {
					blockers = append(blockers, "requested architecture conflicts with environment")
				}
				arch = target
			}
			preferred = asString(env["node"])
		}
		chosenImage = image
		nodes, err := db.queryLocked("SELECT id,last_seen,capabilities FROM nodes ORDER BY id")
		if err != nil {
			return err
		}
		now := stamp()
		for _, node := range nodes {
			id := asString(node["id"])
			caps := decodeObject(asString(node["capabilities"]))
			cpuTotal, memoryTotal := asFloat(caps["cpu"]), asFloat(caps["memory_mb"])
			usedWorkspace, err := db.lockedOne(`SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m
				FROM workspaces WHERE node=? AND status IN ('starting','ready','stopping','failed')`, id)
			if err != nil {
				return err
			}
			usedJobs, err := db.lockedOne(`SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m
				FROM jobs WHERE node=? AND status='leased'`, id)
			if err != nil {
				return err
			}
			cpuFree := math.Max(0, cpuTotal-asFloat(usedWorkspace["c"])-asFloat(usedJobs["c"]))
			memoryFree := math.Max(0, memoryTotal-asFloat(usedWorkspace["m"])-asFloat(usedJobs["m"]))
			activeLeases, err := db.queryLocked("SELECT device_id FROM hardware_leases WHERE node=? AND released IS NULL", id)
			if err != nil {
				return err
			}
			busy := map[string]bool{}
			for _, lease := range activeLeases {
				busy[asString(lease["device_id"])] = true
			}
			allHardware := map[string]int{}
			freeHardware := map[string]int{}
			for _, device := range devicesIn(caps) {
				kind := asString(device["kind"])
				allHardware[kind]++
				if busy[asString(device["id"])] {
					continue
				}
				if kind == "usb" && req.USBPassthrough && asString(device["id"]) != req.USBDeviceID {
					continue
				}
				if kind == "gpu" && req.MinGPUMemoryMB > 0 && asFloat(device["memory_mb"]) < float64(req.MinGPUMemoryMB) {
					continue
				}
				freeHardware[kind]++
			}
			reasons := append([]string(nil), blockers...)
			if now-asFloat(node["last_seen"]) >= 90 {
				reasons = append(reasons, "worker heartbeat expired")
			}
			if !asBool(caps["kubernetes"]) && !asBool(caps["docker"]) {
				reasons = append(reasons, "worker has no approved Kubernetes or Docker executor")
			}
			if asBool(caps["kubernetes"]) && asString(caps["project_id"]) != req.ProjectID {
				reasons = append(reasons, "Kubernetes namespace is reserved for another project")
			}
			if req.NodeID != "" && id != req.NodeID {
				reasons = append(reasons, "worker does not match requested node")
			}
			if req.USBPassthrough && id != req.USBNode {
				reasons = append(reasons, "USB device is bound to a different worker")
			}
			if preferred != "" && id != preferred {
				reasons = append(reasons, "environment image resides on a different worker")
			}
			if arch != "" && normalizeArchitecture(asString(caps["arch"])) != arch {
				reasons = append(reasons, "worker architecture mismatch")
			}
			if req.Environment == "" && !workerHasImage(caps, image) {
				reasons = append(reasons, "worker has not reported approval for the requested OCI image")
			}
			if cpuFree < req.CPU {
				reasons = append(reasons, "insufficient unreserved CPU capacity")
			}
			if memoryFree < float64(req.MemoryMB) {
				reasons = append(reasons, "insufficient unreserved memory capacity")
			}
			for _, kind := range []string{"gpu", "npu", "fpga", "usb"} {
				if hardware[kind] > freeHardware[kind] {
					reasons = append(reasons, "insufficient free or suitable "+kind+" devices")
				}
			}
			eligible := len(reasons) == 0
			score := float64(0)
			if hardware["gpu"] == 0 {
				score += float64(allHardware["gpu"]) * 10000
			}
			if hardware["npu"] == 0 {
				score += float64(allHardware["npu"]) * 2000
			}
			if hardware["fpga"] == 0 {
				score += float64(allHardware["fpga"]) * 2000
			}
			score += math.Max(0, cpuFree-req.CPU)*50 + math.Max(0, memoryFree-float64(req.MemoryMB))/1024
			candidates = append(candidates, candidate{score: score, values: map[string]any{
				"node": id, "adapter": "docker", "online": now-asFloat(node["last_seen"]) < 90,
				"eligible": eligible, "reasons": reasons, "architecture": normalizeArchitecture(asString(caps["arch"])),
				"cpu_total": cpuTotal, "memory_mb_total": memoryTotal, "cpu_unreserved": cpuFree,
				"memory_mb_unreserved": memoryFree, "hardware_total": allHardware,
				"hardware_unreserved": freeHardware, "last_seen": node["last_seen"],
			}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.values["eligible"] != right.values["eligible"] {
			return left.values["eligible"] == true
		}
		if left.score != right.score {
			return left.score < right.score
		}
		return asString(left.values["node"]) < asString(right.values["node"])
	})
	options := make([]map[string]any, 0, len(candidates))
	eligibleCount := 0
	for _, c := range candidates {
		if c.values["eligible"] == true {
			eligibleCount++
			if recommended == "" {
				recommended = asString(c.values["node"])
			}
			c.values["rank"] = eligibleCount
		}
		options = append(options, c.values)
	}
	return map[string]any{
		"status":  map[bool]string{true: "candidates", false: "no_match"}[eligibleCount > 0],
		"project": req.ProjectID, "image": chosenImage,
		"requested_cpu": req.CPU, "requested_memory_mb": req.MemoryMB,
		"hardware": hardware, "min_gpu_memory_mb": req.MinGPUMemoryMB,
		"eligible_count": eligibleCount, "recommended_node": recommended,
		"candidates": options, "blockers": blockers,
		"advisory": "This is a snapshot, not a reservation. Worker capacity, device ownership and image approval are checked again during allocation.",
	}, nil
}
