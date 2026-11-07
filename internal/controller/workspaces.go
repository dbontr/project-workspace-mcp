package controller

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

type WorkspaceRequest struct {
	ProjectID       string         `json:"project_id"`
	Image           string         `json:"image"`
	Environment     string         `json:"environment"`
	ImagePoolID     string         `json:"image_pool_id"`
	NodeID          string         `json:"node_id"`
	Architecture    string         `json:"architecture"`
	MinGPUMemoryMB  int            `json:"min_gpu_memory_mb"`
	CPU             float64        `json:"cpu"`
	MemoryMB        int            `json:"memory_mb"`
	DurationMinutes int            `json:"duration_minutes"`
	Hardware        map[string]any `json:"hardware"`
	USBPassthrough  bool           `json:"usb_passthrough"`
	USBNode         string         `json:"usb_node"`
	USBDeviceID     string         `json:"usb_device_id"`
	Display         bool           `json:"display"`
	DisplayWidth    int            `json:"display_width"`
	DisplayHeight   int            `json:"display_height"`
}

func (r *WorkspaceRequest) defaults() {
	if r.Image == "" {
		r.Image = "debian:bookworm-slim"
	}
	if r.CPU == 0 {
		r.CPU = 1
	}
	if r.MemoryMB == 0 {
		r.MemoryMB = 512
	}
	if r.DurationMinutes == 0 {
		r.DurationMinutes = 120
	}
	if r.Display {
		if r.DisplayWidth == 0 {
			r.DisplayWidth = 1440
		}
		if r.DisplayHeight == 0 {
			r.DisplayHeight = 900
		}
	}
}
func (r WorkspaceRequest) validatedHardware() (map[string]int, error) {
	if !validImage.MatchString(r.Image) || r.CPU < 0.1 || r.CPU > 32 ||
		r.MemoryMB < 64 || r.MemoryMB > 131072 ||
		r.DurationMinutes < 1 || r.DurationMinutes > 1440 {
		return nil, errors.New("invalid workspace limits")
	}
	if r.Display {
		if r.DisplayWidth < 320 || r.DisplayWidth > 3840 ||
			r.DisplayHeight < 240 || r.DisplayHeight > 2160 ||
			r.DisplayWidth*r.DisplayHeight > 8_000_000 {
			return nil, errors.New("invalid virtual display dimensions")
		}
	} else if r.DisplayWidth != 0 || r.DisplayHeight != 0 {
		return nil, errors.New("display dimensions require an enabled virtual display")
	}
	hardware, err := validHardwareRequest(r.Hardware)
	if err != nil {
		return nil, err
	}
	if r.USBPassthrough {
		if !validID.MatchString(r.USBNode) || !validDeviceID.MatchString(r.USBDeviceID) || hardware["usb"] > 1 {
			return nil, errors.New("USB requires an exact node and device ID")
		}
		hardware["usb"] = 1
	} else if hardware["usb"] > 0 || r.USBNode != "" || r.USBDeviceID != "" {
		return nil, errors.New("USB passthrough must be explicitly enabled")
	}
	if r.NodeID != "" && !validID.MatchString(r.NodeID) {
		return nil, errors.New("invalid exact worker ID")
	}
	if r.NodeID != "" && r.USBPassthrough && r.NodeID != r.USBNode {
		return nil, errors.New("USB worker conflicts with required node")
	}
	if r.Architecture != "" && r.Architecture != "amd64" && r.Architecture != "arm64" {
		return nil, errors.New("invalid worker architecture")
	}
	if r.Environment != "" && !validID.MatchString(r.Environment) {
		return nil, errors.New("invalid environment")
	}
	if r.ImagePoolID != "" && (!validID.MatchString(r.ImagePoolID) || r.Environment != "") {
		return nil, errors.New("invalid image pool selection")
	}
	if r.MinGPUMemoryMB < 0 || r.MinGPUMemoryMB > 1000000 ||
		r.MinGPUMemoryMB > 0 && hardware["gpu"] == 0 {
		return nil, errors.New("minimum GPU VRAM requires a GPU allocation")
	}
	return hardware, nil
}

func (db *Database) CreateWorkspace(req WorkspaceRequest) (map[string]any, error) {
	req.defaults()
	hardware, e := req.validatedHardware()
	if e != nil {
		return nil, e
	}
	var out map[string]any
	err := db.transaction(func() error {
		if e := requireProject(db, req.ProjectID); e != nil {
			return e
		}
		preferred, arch := "", req.Architecture
		if req.ImagePoolID != "" {
			image, e := db.lockedOne("SELECT image,architecture FROM pool_images WHERE id=?", req.ImagePoolID)
			if e != nil {
				return e
			}
			if image == nil {
				return errors.New("image not found in approved pool")
			}
			status, _, e := db.poolStateLocked("image", req.ImagePoolID)
			if e != nil {
				return e
			}
			if status != "active" {
				return errors.New("image has been disabled or retired")
			}
			req.Image = asString(image["image"])
			if target := asString(image["architecture"]); target != "" {
				if arch != "" && arch != target {
					return errors.New("requested architecture conflicts with image")
				}
				arch = target
			}
		}
		if req.Environment != "" {
			p, e := db.lockedOne("SELECT * FROM environments WHERE project=? AND name=?", req.ProjectID, req.Environment)
			if e != nil {
				return e
			}
			if p == nil || asString(p["status"]) != "ready" {
				return errors.New("environment is not ready")
			}
			req.Image = asString(p["image"])
			preferred = asString(p["node"])
			if target := asString(p["architecture"]); target != "" {
				if arch != "" && arch != target {
					return errors.New("requested architecture conflicts with environment")
				}
				arch = target
			}
		}
		nodes, e := db.queryLocked("SELECT id,capabilities FROM nodes WHERE last_seen>? ORDER BY id", stamp()-90)
		if e != nil {
			return e
		}
		var nodeID string
		var selected []map[string]any
		for _, node := range nodes {
			id := asString(node["id"])
			if req.USBPassthrough && id != req.USBNode || preferred != "" && id != preferred || req.NodeID != "" && id != req.NodeID {
				continue
			}
			caps := decodeObject(asString(node["capabilities"]))
			if req.ImagePoolID != "" && !workerHasImage(caps, req.Image) ||
				req.Environment == "" && caps["images"] != nil && !workerHasImage(caps, req.Image) {
				continue
			}
			if (!asBool(caps["kubernetes"]) && !asBool(caps["docker"])) ||
				asBool(caps["kubernetes"]) && asString(caps["project_id"]) != req.ProjectID ||
				asFloat(caps["cpu"]) < req.CPU || asFloat(caps["memory_mb"]) < float64(req.MemoryMB) {
				continue
			}
			nodeArch := asString(caps["arch"])
			if nodeArch == "x86_64" {
				nodeArch = "amd64"
			}
			if nodeArch == "aarch64" {
				nodeArch = "arm64"
			}
			if arch != "" && nodeArch != arch {
				continue
			}
			occupied, e := db.lockedOne("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM workspaces WHERE node=? AND status IN ('starting','ready','stopping','failed')", id)
			if e != nil {
				return e
			}
			jobs, e := db.lockedOne("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM jobs WHERE node=? AND status='leased'", id)
			if e != nil {
				return e
			}
			if asFloat(occupied["c"])+asFloat(jobs["c"])+req.CPU > asFloat(caps["cpu"]) || asFloat(occupied["m"])+asFloat(jobs["m"])+float64(req.MemoryMB) > asFloat(caps["memory_mb"]) {
				continue
			}
			leaseRows, e := db.queryLocked("SELECT device_id FROM hardware_leases WHERE node=? AND released IS NULL", id)
			if e != nil {
				return e
			}
			busy := map[string]bool{}
			for _, r := range leaseRows {
				busy[asString(r["device_id"])] = true
			}
			jobLeases, err := db.queryLocked("SELECT device_id FROM task_job_device_leases WHERE node=? AND released IS NULL", id)
			if err != nil {
				return err
			}
			for _, lease := range jobLeases {
				busy[asString(lease["device_id"])] = true
			}
			possible := []map[string]any{}
			for _, d := range devicesIn(caps) {
				if busy[asString(d["id"])] {
					continue
				}
				if asString(d["kind"]) == "usb" && req.USBPassthrough && asString(d["id"]) != req.USBDeviceID {
					continue
				}
				if asString(d["kind"]) == "gpu" && req.MinGPUMemoryMB > 0 &&
					asFloat(d["memory_mb"]) < float64(req.MinGPUMemoryMB) {
					continue
				}
				possible = append(possible, d)
			}
			chosen := []map[string]any{}
			ok := true
			for _, kind := range []string{"fpga", "gpu", "npu", "usb"} {
				need := hardware[kind]
				for _, d := range possible {
					if asString(d["kind"]) == kind && need > 0 {
						chosen = append(chosen, d)
						need--
					}
				}
				if need > 0 {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			nodeID = id
			selected = chosen
			break
		}
		if nodeID == "" {
			return errors.New("no online worker with available capacity and hardware")
		}
		id := randomHex(16)
		now := stamp()
		expires := now + float64(req.DurationMinutes*60)
		if e := db.execLocked("INSERT INTO workspaces VALUES(?,?,?,?,?,?,?,?,?,?)", id, req.ProjectID, nodeID, req.Image, req.CPU, req.MemoryMB, "starting", expires, now, now); e != nil {
			return e
		}
		devices := []map[string]any{}
		for _, d := range selected {
			device := map[string]any{"id": d["id"], "kind": d["kind"]}
			devices = append(devices, device)
			if e := db.execLocked("INSERT INTO hardware_leases VALUES(?,?,?,?,?,NULL)", id, nodeID, d["id"], d["kind"], now); e != nil {
				return e
			}
		}
		_, e = db.EnqueueLocked(id, nodeID, "start", map[string]any{"image": req.Image, "cpu": req.CPU, "memory_mb": req.MemoryMB, "devices": devices, "usb_passthrough": req.USBPassthrough, "display": req.Display, "display_width": req.DisplayWidth, "display_height": req.DisplayHeight})
		if e != nil {
			return e
		}
		out = map[string]any{"id": id, "project": req.ProjectID, "node": nodeID, "status": "starting", "expires": expires, "hardware": devices, "environment": nil, "display": req.Display, "display_width": req.DisplayWidth, "display_height": req.DisplayHeight}
		if req.Environment != "" {
			out["environment"] = req.Environment
		}
		if req.ImagePoolID != "" {
			out["image_pool_id"] = req.ImagePoolID
		}
		return nil
	})
	return out, err
}
func (db *Database) expireLocked(now float64) error {
	if e := db.execLocked("UPDATE operations SET status='expired',token_hash=NULL,updated=? WHERE status='leased' AND lease_until<?", now, now); e != nil {
		return e
	}
	if e := db.execLocked("UPDATE workspaces SET status='failed',updated=? WHERE status='starting' AND EXISTS (SELECT 1 FROM operations WHERE workspace=workspaces.id AND action='start' AND status='expired')", now); e != nil {
		return e
	}
	if e := db.execLocked(`UPDATE checkpointed_stops SET status='failed',updated=?
		WHERE status='pending' AND EXISTS(SELECT 1 FROM operations
		WHERE id=checkpointed_stops.snapshot_operation AND status IN ('expired','failed','cancelled'))`, now); e != nil {
		return e
	}
	if e := db.execLocked(`UPDATE checkpointed_stops SET status='expired',updated=?
		WHERE status IN ('pending','failed') AND workspace IN(SELECT id FROM workspaces
		WHERE expires<? AND status IN ('starting','ready'))`, now, now); e != nil {
		return e
	}
	rows, e := db.queryLocked("SELECT id,node FROM workspaces WHERE expires<? AND status IN ('starting','ready')", now)
	if e != nil {
		return e
	}
	for _, row := range rows {
		ws := asString(row["id"])
		if e := db.execLocked("UPDATE operations SET status='cancelled',updated=? WHERE workspace=? AND status='queued'", now, ws); e != nil {
			return e
		}
		if e := db.execLocked("UPDATE workspaces SET status='stopping',updated=? WHERE id=?", now, ws); e != nil {
			return e
		}
		if _, e := db.EnqueueLocked(ws, asString(row["node"]), "stop", map[string]any{}); e != nil {
			return e
		}
	}
	// Keep an unverified hardware lease until a worker confirms teardown.
	// A lost stop lease must remain retryable after a worker restart.
	stale, e := db.queryLocked(`SELECT id,node FROM workspaces w
		WHERE status='stopping' AND updated<?
		AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.workspace=w.id
			AND o.action='stop' AND o.status IN ('queued','leased'))
		ORDER BY updated,id LIMIT 100`, now-60)
	if e != nil {
		return e
	}
	for _, item := range stale {
		id := asString(item["id"])
		if _, e := db.EnqueueLocked(id, asString(item["node"]), "stop", map[string]any{}); e != nil {
			return e
		}
		if e := db.execLocked("UPDATE workspaces SET updated=? WHERE id=? AND status='stopping'", now, id); e != nil {
			return e
		}
	}
	return nil
}
func (db *Database) ClaimOperation(node string) (map[string]any, error) {
	var out map[string]any
	err := db.transaction(func() error {
		now := stamp()
		if e := db.expireLocked(now); e != nil {
			return e
		}
		row, e := db.lockedOne("SELECT * FROM operations o WHERE node=? AND status='queued' AND NOT EXISTS(SELECT 1 FROM operations x WHERE x.workspace=o.workspace AND x.status='leased') ORDER BY created,id LIMIT 1", node)
		if e != nil || row == nil {
			return e
		}
		token := randomHex(32)
		if e = db.execLocked("UPDATE operations SET status='leased',token_hash=?,lease_until=?,updated=? WHERE id=?", hashToken(token), now+420, now, row["id"]); e != nil {
			return e
		}
		out = map[string]any{"id": row["id"], "workspace": row["workspace"], "action": row["action"], "payload": decodeObject(asString(row["payload"])), "lease_token": token}
		return nil
	})
	return out, err
}
func (db *Database) validateOperationLeaseLocked(node, op, token string) (map[string]any, error) {
	row, e := db.lockedOne("SELECT * FROM operations WHERE id=? AND node=? AND status='leased' AND lease_until>?", op, node, stamp())
	if e != nil {
		return nil, e
	}
	if row == nil || !equalSecret(asString(row["token_hash"]), hashToken(token)) {
		return nil, errors.New("invalid or expired operation lease")
	}
	return row, nil
}
func (db *Database) RenewOperation(node, op, token string) (map[string]any, error) {
	var out map[string]any
	err := db.transaction(func() error {
		row, e := db.validateOperationLeaseLocked(node, op, token)
		if e != nil {
			return e
		}
		ws, e := db.lockedOne("SELECT expires FROM workspaces WHERE id=?", row["workspace"])
		if e != nil {
			return e
		}
		until := min(stamp()+420, asFloat(ws["expires"])+120)
		if until <= stamp() {
			return errors.New("workspace lease expired")
		}
		if e = db.execLocked("UPDATE operations SET lease_until=?,updated=? WHERE id=?", until, stamp(), op); e != nil {
			return e
		}
		out = map[string]any{"lease_until": until}
		return nil
	})
	return out, err
}
func (db *Database) FinishOperation(node, id, token string, success bool, result map[string]any) (map[string]any, error) {
	if len(encode(result)) > 3_000_000 {
		return nil, errors.New("operation result too large")
	}
	var out map[string]any
	err := db.transaction(func() error {
		op, e := db.validateOperationLeaseLocked(node, id, token)
		if e != nil {
			return e
		}
		ws, e := db.lockedOne("SELECT * FROM workspaces WHERE id=?", op["workspace"])
		if e != nil {
			return e
		}
		action := asString(op["action"])
		payload := decodeObject(asString(op["payload"]))
		project := asString(ws["project"])
		if success && (action == "export_file" || action == "snapshot") && result["base64"] != nil {
			data, e := base64.StdEncoding.DecodeString(asString(result["base64"]))
			if e != nil || len(data) > 1<<20 {
				return errors.New("invalid inline artifact")
			}
			meta, e := db.artifactBytesLocked(project, asString(result["name"]), data)
			if e != nil {
				return e
			}
			result = map[string]any{"artifact": meta}
		}
		if action == "capture_display" && success && result["artifact_id"] == nil {
			return errors.New("display capture requires an uploaded PNG artifact")
		}
		if success && (action == "export_file" || action == "snapshot" || action == "export_image" || action == "capture_display") && result["artifact_id"] != nil {
			linked, e := db.lockedOne("SELECT 1 present FROM operation_artifacts WHERE operation=? AND artifact=?", id, result["artifact_id"])
			if e != nil {
				return e
			}
			if linked == nil {
				return errors.New("unverified operation artifact")
			}
			artifact, e := db.lockedOne("SELECT id,name,sha256,size FROM artifacts WHERE id=? AND project=?", result["artifact_id"], project)
			if e != nil {
				return e
			}
			if artifact == nil {
				return errors.New("missing uploaded artifact")
			}
			if action == "capture_display" {
				width, height := asInt(result["width"]), asInt(result["height"])
				if asString(artifact["name"]) != "display.png" ||
					width < 320 || width > 3840 || height < 240 || height > 2160 ||
					width*height > 8_000_000 || asFloat(artifact["size"]) > 8<<20 {
					return errors.New("invalid display capture artifact")
				}
				result = map[string]any{"artifact": artifact, "width": width, "height": height, "mime_type": "image/png"}
			} else {
				result = map[string]any{"artifact": artifact}
			}
		}
		if action == "import_archive" && success {
			if e := db.execLocked("INSERT INTO workspace_repositories VALUES (?,?,?) ON CONFLICT(workspace,repository) DO UPDATE SET revision=excluded.revision", ws["id"], payload["repository"], payload["revision"]); e != nil {
				return e
			}
		}
		var snapshotID string
		if action == "snapshot" && success {
			artifact, _ := result["artifact"].(map[string]any)
			if artifact == nil {
				return errors.New("snapshot requires durable artifact")
			}
			snapshotID = randomHex(16)
			if e := db.execLocked("INSERT INTO workspace_snapshots VALUES(?,?,?,?)", snapshotID, ws["id"], artifact["id"], stamp()); e != nil {
				return e
			}
		}
		if action == "export_image" && success {
			artifact, _ := result["artifact"].(map[string]any)
			if artifact == nil {
				return errors.New("image export requires artifact")
			}
			profile, e := db.lockedOne("SELECT version FROM environments WHERE project=? AND name=?", project, payload["name"])
			if e != nil {
				return e
			}
			if profile == nil || asInt(profile["version"]) != asInt(payload["version"]) {
				return errors.New("environment version changed")
			}
			if e := db.execLocked("INSERT INTO environment_exports VALUES(?,?,?,?,?)", artifact["id"], project, payload["name"], payload["version"], stamp()); e != nil {
				return e
			}
		}
		if action == "load_image" && success {
			profile, e := db.lockedOne("SELECT * FROM environments WHERE project=? AND name=?", project, payload["name"])
			if e != nil {
				return e
			}
			if profile == nil || asInt(profile["version"]) != asInt(payload["version"]) || result["image"] != profile["image"] || result["image_id"] == nil {
				return errors.New("image identity mismatch")
			}
			if e := db.execLocked("UPDATE environments SET status='ready',node=?,image_id=?,updated=? WHERE project=? AND name=?", ws["node"], result["image_id"], stamp(), project, payload["name"]); e != nil {
				return e
			}
		}
		if action == "build_image" {
			profile, e := db.lockedOne("SELECT * FROM environments WHERE project=? AND name=?", project, payload["name"])
			if e != nil {
				return e
			}
			if profile != nil && asInt(profile["version"]) == asInt(payload["version"]) && profile["node"] == ws["node"] {
				if success && result["image"] == profile["image"] && result["image_id"] != nil {
					e = db.execLocked("UPDATE environments SET status='ready',image_id=?,updated=? WHERE project=? AND name=?", result["image_id"], stamp(), project, payload["name"])
				} else {
					success = false
					e = db.execLocked("UPDATE environments SET status='failed',updated=? WHERE project=? AND name=?", stamp(), project, payload["name"])
				}
				if e != nil {
					return e
				}
			}
		}
		status := "failed"
		if success {
			status = "succeeded"
		}
		if e := db.execLocked("UPDATE operations SET status=?,result=?,updated=?,token_hash=NULL WHERE id=?", status, encode(result), stamp(), id); e != nil {
			return e
		}
		if e := db.finishCheckpointStopLocked(ws, op, success, snapshotID); e != nil {
			return e
		}
		if action == "start" || action == "stop" {
			state := "failed"
			if success {
				if action == "start" {
					state = "ready"
				} else {
					state = "stopped"
				}
			}
			if e := db.execLocked("UPDATE workspaces SET status=?,updated=? WHERE id=?", state, stamp(), ws["id"]); e != nil {
				return e
			}
			if action == "stop" && success {
				if e := db.execLocked("UPDATE hardware_leases SET released=? WHERE workspace=? AND released IS NULL", stamp(), ws["id"]); e != nil {
					return e
				}
			}
		}
		out = map[string]any{"id": id, "status": status, "result": result}
		return nil
	})
	return out, err
}
func (db *Database) WorkspaceRepositories(project, ws string) ([]map[string]any, error) {
	if _, e := db.Workspace(project, ws); e != nil {
		return nil, e
	}
	return db.query("SELECT repository,revision FROM workspace_repositories WHERE workspace=?", ws)
}
func (db *Database) QueueImportFile(project, ws, artifact, path string) (map[string]any, error) {
	if !validRelativePath(path) {
		return nil, errors.New("invalid workspace destination")
	}
	var id string
	err := db.transaction(func() error {
		row, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		a, e := db.lockedOne("SELECT id FROM artifacts WHERE id=? AND project=?", artifact, project)
		if e != nil {
			return e
		}
		if a == nil {
			return errors.New("artifact not found")
		}
		if e := db.execLocked("INSERT OR IGNORE INTO artifact_grants VALUES(?,?)", ws, artifact); e != nil {
			return e
		}
		id, e = db.EnqueueLocked(ws, asString(row["node"]), "import_file", map[string]any{"artifact_id": artifact, "path": path})
		return e
	})
	return map[string]any{"operation_id": id, "status": "queued"}, err
}
func (db *Database) QueueImportRepository(project, ws, repo, revision, artifact string) (map[string]any, error) {
	var id string
	err := db.transaction(func() error {
		row, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		linked, e := db.lockedOne("SELECT 1 present FROM repositories WHERE project=? AND full_name=?", project, repo)
		if e != nil {
			return e
		}
		if linked == nil {
			return errors.New("repository not linked")
		}
		a, e := db.lockedOne("SELECT 1 present FROM artifacts WHERE project=? AND id=?", project, artifact)
		if e != nil {
			return e
		}
		if a == nil {
			return errors.New("artifact not found")
		}
		if e := db.execLocked("INSERT OR IGNORE INTO artifact_grants VALUES (?,?)", ws, artifact); e != nil {
			return e
		}
		id, e = db.EnqueueLocked(ws, asString(row["node"]), "import_archive", map[string]any{"artifact_id": artifact, "repository": repo, "revision": revision})
		return e
	})
	return map[string]any{"operation_id": id, "status": "queued"}, err
}
func (db *Database) SnapshotList(project, ws string) ([]map[string]any, error) {
	if _, e := db.Workspace(project, ws); e != nil {
		return nil, e
	}
	return db.query("SELECT id,workspace,artifact,created FROM workspace_snapshots WHERE workspace=? ORDER BY created DESC", ws)
}
func (db *Database) RestoreSnapshot(project, ws, snapshot string) (map[string]any, error) {
	var id string
	err := db.transaction(func() error {
		row, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		s, e := db.lockedOne("SELECT s.artifact FROM workspace_snapshots s JOIN workspaces w ON w.id=s.workspace WHERE s.id=? AND w.project=?", snapshot, project)
		if e != nil {
			return e
		}
		if s == nil {
			return errors.New("snapshot not found")
		}
		n, e := db.countLocked("SELECT count(*) n FROM operations WHERE workspace=? AND status IN ('queued','leased')", ws)
		if e != nil {
			return e
		}
		if n >= 32 {
			return errors.New("workspace operation queue full")
		}
		if e := db.execLocked("INSERT OR IGNORE INTO artifact_grants VALUES(?,?)", ws, s["artifact"]); e != nil {
			return e
		}
		id, e = db.EnqueueLocked(ws, asString(row["node"]), "restore_snapshot", map[string]any{"artifact_id": s["artifact"]})
		return e
	})
	return map[string]any{"operation_id": id, "status": "queued"}, err
}
func (db *Database) EnvironmentDefine(project, name, base, file, context, arch string, expected int) (map[string]any, error) {
	if !validID.MatchString(name) || !validImage.MatchString(base) || len(file) > 16384 || context != "" && !validID.MatchString(context) || arch != "" && arch != "amd64" && arch != "arm64" {
		return nil, errors.New("invalid environment definition")
	}
	if file != "" {
		lines := []string{}
		count := 0
		for _, line := range strings.Split(file, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			lines = append(lines, line)
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.EqualFold(fields[0], "FROM") {
				count++
			}
		}
		if len(lines) == 0 || len(strings.Fields(lines[0])) != 2 || !strings.EqualFold(strings.Fields(lines[0])[0], "FROM") || strings.Fields(lines[0])[1] != base || count != 1 {
			return nil, errors.New("Dockerfile must start with one approved FROM")
		}
	}
	var result map[string]any
	err := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		row, e := db.lockedOne("SELECT version FROM environments WHERE project=? AND name=?", project, name)
		if e != nil {
			return e
		}
		ver := 0
		if row != nil {
			ver = asInt(row["version"])
		}
		if expected != ver {
			return errors.New("environment version conflict")
		}
		now := stamp()
		image := fmt.Sprintf("pwm-env-%s-%s:v%d", project, name, ver+1)
		e = db.execLocked("INSERT INTO environments(project,name,version,base_image,dockerfile,image,context_name,architecture,node,status,image_id,created,updated) VALUES(?,?,?,?,?,?,?,?,NULL,'defined',NULL,?,?) ON CONFLICT(project,name) DO UPDATE SET version=excluded.version,base_image=excluded.base_image,dockerfile=excluded.dockerfile,image=excluded.image,context_name=excluded.context_name,architecture=excluded.architecture,node=NULL,status='defined',image_id=NULL,updated=excluded.updated", project, name, ver+1, base, file, image, context, arch, now, now)
		if e != nil {
			return e
		}
		result = map[string]any{"project": project, "name": name, "version": ver + 1, "image": image, "status": "defined", "context": context, "architecture": arch}
		return nil
	})
	return result, err
}
func (db *Database) Environments(project string) ([]map[string]any, error) {
	return db.query("SELECT project,name,version,base_image,image,context_name,architecture,node,status,image_id,created,updated FROM environments WHERE project=? ORDER BY name", project)
}
func (db *Database) EnvironmentAction(project, name, ws, action, artifact string) (map[string]any, error) {
	if action != "build_image" && action != "export_image" && action != "load_image" {
		return nil, errors.New("invalid environment action")
	}
	var out map[string]any
	err := db.transaction(func() error {
		w, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		p, e := db.lockedOne("SELECT * FROM environments WHERE project=? AND name=?", project, name)
		if e != nil {
			return e
		}
		if p == nil {
			return errors.New("environment not found")
		}
		if action == "build_image" {
			if asString(p["status"]) == "ready" || asString(p["status"]) == "building" {
				return errors.New("environment already built or building")
			}
			if arch := asString(p["architecture"]); arch != "" {
				n, e := db.lockedOne("SELECT capabilities FROM nodes WHERE id=?", w["node"])
				if e != nil {
					return e
				}
				actual := asString(decodeObject(asString(n["capabilities"]))["arch"])
				if actual == "x86_64" {
					actual = "amd64"
				}
				if actual == "aarch64" {
					actual = "arm64"
				}
				if actual != arch {
					return errors.New("environment architecture mismatch")
				}
			}
		}
		if action == "export_image" && (asString(p["status"]) != "ready" || p["node"] != w["node"]) {
			return errors.New("environment image unavailable on this worker")
		}
		if action == "load_image" {
			linked, e := db.lockedOne("SELECT version FROM environment_exports WHERE artifact=? AND project=? AND name=?", artifact, project, name)
			if e != nil {
				return e
			}
			if linked == nil || linked["version"] != p["version"] {
				return errors.New("environment export version mismatch")
			}
			if e := db.execLocked("INSERT OR IGNORE INTO artifact_grants VALUES(?,?)", ws, artifact); e != nil {
				return e
			}
		}
		payload := map[string]any{"name": name, "version": p["version"], "image": p["image"]}
		if action == "build_image" {
			payload["base_image"] = p["base_image"]
			payload["dockerfile"] = p["dockerfile"]
			payload["context"] = p["context_name"]
			payload["architecture"] = p["architecture"]
		}
		if action == "load_image" {
			payload["artifact_id"] = artifact
		}
		id, e := db.EnqueueLocked(ws, asString(w["node"]), action, payload)
		if e != nil {
			return e
		}
		if action == "build_image" {
			if e := db.execLocked("UPDATE environments SET status='building',node=?,updated=? WHERE project=? AND name=?", w["node"], stamp(), project, name); e != nil {
				return e
			}
		}
		out = map[string]any{"operation_id": id, "status": "queued"}
		if action == "build_image" {
			out["image"] = p["image"]
		}
		return nil
	})
	return out, err
}
