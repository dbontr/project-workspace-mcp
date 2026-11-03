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
	CPU             float64        `json:"cpu"`
	MemoryMB        int            `json:"memory_mb"`
	DurationMinutes int            `json:"duration_minutes"`
	Hardware        map[string]any `json:"hardware"`
	USBPassthrough  bool           `json:"usb_passthrough"`
	USBNode         string         `json:"usb_node"`
	USBDeviceID     string         `json:"usb_device_id"`
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
}
func (db *Database) CreateWorkspace(req WorkspaceRequest) (map[string]any, error) {
	req.defaults()
	if !validImage.MatchString(req.Image) || req.CPU < 0.1 || req.CPU > 32 || req.MemoryMB < 64 || req.MemoryMB > 131072 || req.DurationMinutes < 1 || req.DurationMinutes > 1440 {
		return nil, errors.New("invalid workspace limits")
	}
	hardware, e := validHardwareRequest(req.Hardware)
	if e != nil {
		return nil, e
	}
	if req.USBPassthrough {
		if !validID.MatchString(req.USBNode) || !validDeviceID.MatchString(req.USBDeviceID) || hardware["usb"] > 1 {
			return nil, errors.New("USB requires an exact node and device ID")
		}
		hardware["usb"] = 1
	} else if hardware["usb"] > 0 || req.USBNode != "" || req.USBDeviceID != "" {
		return nil, errors.New("USB passthrough must be explicitly enabled")
	}
	if req.Environment != "" && !validID.MatchString(req.Environment) {
		return nil, errors.New("invalid environment")
	}
	var out map[string]any
	err := db.transaction(func() error {
		if e := requireProject(db, req.ProjectID); e != nil {
			return e
		}
		preferred, arch := "", ""
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
			arch = asString(p["architecture"])
		}
		nodes, e := db.queryLocked("SELECT id,capabilities FROM nodes WHERE last_seen>? ORDER BY id", stamp()-90)
		if e != nil {
			return e
		}
		var nodeID string
		var selected []map[string]any
		for _, node := range nodes {
			id := asString(node["id"])
			if req.USBPassthrough && id != req.USBNode || preferred != "" && id != preferred {
				continue
			}
			caps := decodeObject(asString(node["capabilities"]))
			if !asBool(caps["docker"]) || asFloat(caps["cpu"]) < req.CPU || asFloat(caps["memory_mb"]) < float64(req.MemoryMB) {
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
			possible := []map[string]any{}
			for _, d := range devicesIn(caps) {
				if busy[asString(d["id"])] {
					continue
				}
				if asString(d["kind"]) == "usb" && req.USBPassthrough && asString(d["id"]) != req.USBDeviceID {
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
		_, e = db.EnqueueLocked(id, nodeID, "start", map[string]any{"image": req.Image, "cpu": req.CPU, "memory_mb": req.MemoryMB, "devices": devices, "usb_passthrough": req.USBPassthrough})
		if e != nil {
			return e
		}
		out = map[string]any{"id": id, "project": req.ProjectID, "node": nodeID, "status": "starting", "expires": expires, "hardware": devices, "environment": nil}
		if req.Environment != "" {
			out["environment"] = req.Environment
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
		if success && (action == "export_file" || action == "snapshot" || action == "export_image") && result["artifact_id"] != nil {
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
			result = map[string]any{"artifact": artifact}
		}
		if action == "import_archive" && success {
			if e := db.execLocked("INSERT INTO workspace_repositories VALUES (?,?,?) ON CONFLICT(workspace,repository) DO UPDATE SET revision=excluded.revision", ws["id"], payload["repository"], payload["revision"]); e != nil {
				return e
			}
		}
		if action == "snapshot" && success {
			artifact, _ := result["artifact"].(map[string]any)
			if artifact == nil {
				return errors.New("snapshot requires durable artifact")
			}
			if e := db.execLocked("INSERT INTO workspace_snapshots VALUES(?,?,?,?)", randomHex(16), ws["id"], artifact["id"], stamp()); e != nil {
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
