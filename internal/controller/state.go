package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

func hashToken(token string) string {
	b := sha256.Sum256([]byte(token))
	return hex.EncodeToString(b[:])
}
func (db *Database) Repositories(project string) ([]string, error) {
	rows, e := db.query("SELECT full_name FROM repositories WHERE project=? ORDER BY full_name", project)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, asString(r["full_name"]))
	}
	return out, nil
}
func (db *Database) LinkRepository(project, repo string) (map[string]any, error) {
	if !validSourceRepository(repo) {
		return nil, errors.New("invalid repository")
	}
	err := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		return db.execLocked("INSERT OR IGNORE INTO repositories VALUES (?,?)", project, repo)
	})
	return map[string]any{"project": project, "repository": repo}, err
}
func (db *Database) RepositoryLinked(project, repo string) bool {
	rows, e := db.query("SELECT 1 present FROM repositories WHERE project=? AND full_name=?", project, repo)
	return e == nil && len(rows) > 0
}
func validateCaps(caps map[string]any) error {
	if caps == nil {
		return errors.New("missing node capabilities")
	}
	for _, field := range []string{"cpu", "memory_mb", "max_jobs"} {
		v := asFloat(caps[field])
		if v <= 0 || v > 1_000_000 {
			return fmt.Errorf("invalid worker capacity %s", field)
		}
	}
	if raw, ok := caps["images"]; ok {
		images, valid := raw.([]any)
		if !valid || len(images) > 128 {
			return errors.New("invalid worker image inventory")
		}
		seenImages := map[string]bool{}
		for _, v := range images {
			image, valid := v.(string)
			if !valid || !validImage.MatchString(image) || seenImages[image] {
				return errors.New("invalid or duplicate approved image")
			}
			seenImages[image] = true
		}
	}
	devices, _ := caps["devices"].([]any)
	if len(devices) > 64 {
		return errors.New("too many devices")
	}
	seen := map[string]bool{}
	for _, item := range devices {
		dev, ok := item.(map[string]any)
		if !ok {
			return errors.New("invalid device description")
		}
		id, kind := asString(dev["id"]), asString(dev["kind"])
		if !validDeviceID.MatchString(id) || seen[id] || !deviceKind(kind) {
			return errors.New("invalid or duplicate device")
		}
		seen[id] = true
		if len(asString(dev["model"])) > 128 || asFloat(dev["memory_mb"]) < 0 {
			return errors.New("invalid device metadata")
		}
	}
	return nil
}
func deviceKind(s string) bool { return s == "gpu" || s == "npu" || s == "fpga" || s == "usb" }
func (db *Database) RegisterNode(name string, caps map[string]any) (string, error) {
	if err := requireID(name); err != nil {
		return "", err
	}
	if err := validateCaps(caps); err != nil {
		return "", err
	}
	token := randomHex(36)
	err := db.exec("INSERT INTO nodes VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,capabilities=excluded.capabilities,last_seen=excluded.last_seen", name, hashToken(token), encode(caps), stamp())
	return token, err
}
func (db *Database) PairingCode() (map[string]any, error) {
	code := randomHex(24)
	now := stamp()
	err := db.transaction(func() error {
		if e := db.execLocked("INSERT INTO pairings VALUES(?,?)", hashToken(code), now+600); e != nil {
			return e
		}
		return db.execLocked("DELETE FROM pairings WHERE expires<?", now)
	})
	return map[string]any{"pairing_code": code, "expires_in_seconds": 600}, err
}
func (db *Database) RedeemCode(code, node string, caps map[string]any) (map[string]any, error) {
	if err := requireID(node); err != nil {
		return nil, err
	}
	if err := validateCaps(caps); err != nil {
		return nil, err
	}
	token := randomHex(36)
	err := db.transaction(func() error {
		r, e := db.lockedOne("SELECT code_hash FROM pairings WHERE code_hash=? AND expires>?", hashToken(code), stamp())
		if e != nil {
			return e
		}
		if r == nil {
			return errors.New("invalid or expired pairing code")
		}
		if e := db.execLocked("DELETE FROM pairings WHERE code_hash=?", hashToken(code)); e != nil {
			return e
		}
		found, e := db.lockedOne("SELECT id FROM nodes WHERE id=?", node)
		if e != nil {
			return e
		}
		if found != nil {
			return errors.New("worker already registered")
		}
		return db.execLocked("INSERT INTO nodes VALUES(?,?,?,?)", node, hashToken(token), encode(caps), stamp())
	})
	return map[string]any{"node_id": node, "worker_token": token}, err
}
func (db *Database) NodeAuthorized(node, token string) bool {
	r, e := db.queryOne("SELECT token_hash FROM nodes WHERE id=?", node)
	return e == nil && r != nil && equalSecret(asString(r["token_hash"]), hashToken(token))
}
func (db *Database) Heartbeat(node string, caps map[string]any) error {
	if err := validateCaps(caps); err != nil {
		return err
	}
	return db.exec("UPDATE nodes SET capabilities=?,last_seen=? WHERE id=?", encode(caps), stamp(), node)
}
func (db *Database) Nodes() ([]map[string]any, error) {
	rows, e := db.query("SELECT id,capabilities,last_seen FROM nodes ORDER BY id")
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, map[string]any{"id": r["id"], "capabilities": decodeObject(asString(r["capabilities"])), "last_seen": r["last_seen"], "online": stamp()-asFloat(r["last_seen"]) < 90})
	}
	return out, nil
}
func (db *Database) Hardware() ([]map[string]any, error) {
	nodes, e := db.Nodes()
	if e != nil {
		return nil, e
	}
	leases, e := db.query("SELECT node,device_id,workspace FROM hardware_leases WHERE released IS NULL")
	if e != nil {
		return nil, e
	}
	busy := map[string]any{}
	for _, l := range leases {
		busy[asString(l["node"])+"/"+asString(l["device_id"])] = l["workspace"]
	}
	jobLeases, e := db.query("SELECT node,device_id,job FROM task_job_device_leases WHERE released IS NULL")
	if e != nil {
		return nil, e
	}
	for _, lease := range jobLeases {
		busy[asString(lease["node"])+"/"+asString(lease["device_id"])] = "job:" + asString(lease["job"])
	}
	out := []map[string]any{}
	for _, n := range nodes {
		cap := n["capabilities"].(map[string]any)
		for _, item := range devicesIn(cap) {
			d := item
			out = append(out, map[string]any{"node": n["id"], "online": n["online"], "id": d["id"], "kind": d["kind"], "model": d["model"], "memory_mb": d["memory_mb"], "leased_to": busy[asString(n["id"])+"/"+asString(d["id"])]})
		}
	}
	return out, nil
}
func devicesIn(cap map[string]any) []map[string]any {
	out := []map[string]any{}
	items, _ := cap["devices"].([]any)
	for _, item := range items {
		d, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"id": d["id"], "kind": d["kind"], "model": d["model"], "memory_mb": d["memory_mb"]})
	}
	sort.Slice(out, func(i, j int) bool { return asString(out[i]["id"]) < asString(out[j]["id"]) })
	return out
}
func (db *Database) Jobs(project string) ([]map[string]any, error) {
	return db.query("SELECT id,project,node,status,created,updated,result FROM jobs WHERE project=? ORDER BY created DESC LIMIT 100", project)
}
func (db *Database) SubmitJob(project, command, image string, cpu float64, memory int) (map[string]any, error) {
	if strings.TrimSpace(command) == "" || len(command) > 16000 || !validImage.MatchString(image) || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0.1 || cpu > 32 || memory < 64 || memory > 131072 {
		return nil, errors.New("invalid job")
	}
	id := randomHex(16)
	err := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		now := stamp()
		return db.execLocked("INSERT INTO jobs(id,project,command,image,cpu,memory_mb,status,created,updated) VALUES(?,?,?,?,?,?,'queued',?,?)", id, project, command, image, cpu, memory, now, now)
	})
	return map[string]any{"id": id, "status": "queued"}, err
}
func (db *Database) ClaimJob(node string) (map[string]any, error) {
	var out map[string]any
	err := db.transaction(func() error {
		now := stamp()
		if e := db.execLocked("UPDATE jobs SET status='expired',updated=? WHERE status='leased' AND lease_until<?", now, now); e != nil {
			return e
		}
		n, e := db.lockedOne("SELECT capabilities FROM nodes WHERE id=?", node)
		if e != nil {
			return e
		}
		if n == nil {
			return errors.New("node not found")
		}
		caps := decodeObject(asString(n["capabilities"]))
		personalAllowed, e := db.personalWorkerAllowedLocked(node)
		if e != nil {
			return e
		}
		if !asBool(caps["docker"]) && !asBool(caps["kubernetes"]) {
			return nil
		}
		occupied, e := db.lockedOne("SELECT count(*) n,coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM jobs WHERE node=? AND status='leased'", node)
		if e != nil {
			return e
		}
		if asInt(occupied["n"]) >= max(1, asInt(caps["max_jobs"])) {
			return nil
		}
		reserved, e := db.lockedOne("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM workspaces WHERE node=? AND status IN ('starting','ready','stopping','failed')", node)
		if e != nil {
			return e
		}
		availableCPU := asFloat(caps["cpu"]) - asFloat(occupied["c"]) - asFloat(reserved["c"])
		availableMem := asFloat(caps["memory_mb"]) - asFloat(occupied["m"]) - asFloat(reserved["m"])
		query := "SELECT * FROM jobs WHERE status='queued' AND cpu<=? AND memory_mb<=?"
		arguments := []any{availableCPU, availableMem}
		if asBool(caps["kubernetes"]) {
			project := asString(caps["project_id"])
			if project == "" {
				return errors.New("Kubernetes worker must be scoped to one project")
			}
			query += " AND project=?"
			arguments = append(arguments, project)
		} else if project := asString(caps["project_id"]); project != "" {
			query += " AND project=?"
			arguments = append(arguments, project)
		}
		if !personalAllowed {
			query += " AND project!=?"
			arguments = append(arguments, personalComputeScope)
		}
		candidates, e := db.queryLocked(query+" ORDER BY created,id LIMIT 200", arguments...)
		if e != nil {
			return e
		}
		var row map[string]any
		var selected []map[string]any
		for _, candidate := range candidates {
			if asString(candidate["project"]) == personalComputeScope &&
				(!personalAllowed || !workerHasImage(caps, asString(candidate["image"]))) {
				continue
			}
			devices, eligible, err := db.jobHardwareSelectionLocked(asString(candidate["id"]), node, caps)
			if err != nil {
				return err
			}
			if !eligible {
				continue
			}
			row, selected = candidate, devices
			break
		}
		if row == nil {
			return nil
		}
		token := randomHex(32)
		if err := db.leaseJobHardwareLocked(asString(row["id"]), node, selected); err != nil {
			return err
		}
		if e = db.execLocked("UPDATE jobs SET status='leased',node=?,token_hash=?,lease_until=?,updated=? WHERE id=?", node, hashToken(token), now+420, now, row["id"]); e != nil {
			return e
		}
		row["devices"] = selected
		row["lease_token"] = token
		out = row
		return nil
	})
	return out, err
}

// ReconcileInterruptedJobs fences claims only after the authenticated worker
// has removed its abandoned runtime jobs. Hardware leases stay reserved until
// their physical cleanup is verified independently.
func (db *Database) ReconcileInterruptedJobs(node string) (map[string]any, error) {
	if !validID.MatchString(node) {
		return nil, errors.New("invalid worker")
	}
	count := 0
	err := db.transaction(func() error {
		row, err := db.lockedOne("SELECT id FROM nodes WHERE id=?", node)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("worker not found")
		}
		active, err := db.lockedOne("SELECT count(*) n FROM jobs WHERE node=? AND status='leased'", node)
		if err != nil {
			return err
		}
		count = asInt(active["n"])
		return db.execLocked("UPDATE jobs SET status='expired',result=?,token_hash=NULL,lease_until=NULL,updated=? WHERE node=? AND status='leased'",
			"worker execution interrupted; runtime cleanup confirmed", stamp(), node)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"node": node, "expired_jobs": count}, nil
}

func (db *Database) FinishJob(node, id, token, status, result string) (map[string]any, error) {
	if status != "succeeded" && status != "failed" {
		return nil, errors.New("invalid status")
	}
	err := db.transaction(func() error {
		row, e := db.lockedOne("SELECT token_hash FROM jobs WHERE id=? AND node=? AND status='leased' AND lease_until>?", id, node, stamp())
		if e != nil {
			return e
		}
		if row == nil || !equalSecret(asString(row["token_hash"]), hashToken(token)) {
			return errors.New("invalid or expired job lease")
		}
		if len(result) > 32768 {
			result = result[:32768]
		}
		if err := db.execLocked("UPDATE jobs SET status=?,result=?,updated=?,lease_until=NULL,token_hash=NULL WHERE id=?", status, result, stamp(), id); err != nil {
			return err
		}
		// A failed worker operation does not prove that its host device was
		// released. The operator must verify cleanup before reuse.
		if status == "succeeded" {
			return db.releaseJobHardwareLocked(id, node)
		}
		return nil
	})
	return map[string]any{"id": id, "status": status}, err
}
func validHardwareRequest(req map[string]any) (map[string]int, error) {
	out := map[string]int{}
	total := 0
	for kind, v := range req {
		if !deviceKind(kind) {
			return nil, errors.New("invalid hardware kind")
		}
		var f float64
		switch value := v.(type) {
		case float64:
			f = value
		case int:
			f = float64(value)
		case int64:
			f = float64(value)
		default:
			return nil, errors.New("invalid hardware quantity")
		}
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > 8 {
			return nil, errors.New("invalid hardware quantity")
		}
		n := int(f)
		if n > 0 {
			out[kind] = n
			total += n
		}
	}
	if total > 8 {
		return nil, errors.New("hardware request exceeds eight devices")
	}
	return out, nil
}
func regexRef(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_./-]{1,160}$`).MatchString(value) && !strings.Contains(value, "..")
}
func (db *Database) Status() (map[string]any, error) {
	result := map[string]any{}
	for _, table := range []string{"projects", "nodes", "jobs", "workspaces", "artifacts"} {
		r, e := db.queryOne("SELECT count(*) n FROM " + table)
		if e != nil {
			return nil, e
		}
		result[table] = r["n"]
	}
	return result, nil
}

func (db *Database) EnqueueLocked(workspace, node, action string, payload any) (string, error) {
	id := randomHex(16)
	now := stamp()
	e := db.execLocked("INSERT INTO operations(id,workspace,node,action,payload,status,created,updated) VALUES(?,?,?,?,?,'queued',?,?)", id, workspace, node, action, encode(payload), now, now)
	return id, e
}
func (db *Database) Workspaces(project string) ([]map[string]any, error) {
	return db.query("SELECT * FROM workspaces WHERE project=? ORDER BY created DESC", project)
}
func (db *Database) Workspace(project, id string) (map[string]any, error) {
	r, e := db.queryOne("SELECT * FROM workspaces WHERE id=? AND project=?", id, project)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, errors.New("workspace not found")
	}
	return r, nil
}
func (db *Database) Operation(project, ws, id string) (map[string]any, error) {
	row, e := db.queryOne("SELECT o.id,o.workspace,o.action,o.status,o.result,o.created,o.updated FROM operations o JOIN workspaces w ON w.id=o.workspace WHERE o.id=? AND w.id=? AND w.project=?", id, ws, project)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("operation not found")
	}
	if row["result"] != nil {
		row["result"] = decodeObject(asString(row["result"]))
	}
	return row, nil
}
func (db *Database) CallWorkspace(project, ws, action string, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"command": true, "read_file": true, "write_file": true, "list_directory": true, "search": true, "export_file": true, "snapshot": true, "capture_display": true}
	if !allowed[action] || len(encode(payload)) > 262144 {
		return nil, errors.New("invalid workspace operation")
	}
	var id string
	err := db.transaction(func() error {
		row, e := db.readyWorkspaceLocked(project, ws)
		if e != nil {
			return e
		}
		busy, e := db.countLocked("SELECT count(*) n FROM operations WHERE workspace=? AND status IN ('queued','leased')", ws)
		if e != nil {
			return e
		}
		if busy >= 32 {
			return errors.New("workspace operation queue full")
		}
		id, e = db.EnqueueLocked(ws, asString(row["node"]), action, payload)
		return e
	})
	return map[string]any{"operation_id": id, "status": "queued"}, err
}
func (db *Database) readyWorkspaceLocked(project, id string) (map[string]any, error) {
	row, e := db.lockedOne("SELECT * FROM workspaces WHERE id=? AND project=? AND status='ready' AND expires>?", id, project, stamp())
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("workspace is not ready")
	}
	pending, err := db.checkpointStopPendingLocked(id)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, errors.New("workspace checkpoint-before-stop is pending")
	}
	return row, nil
}
func (db *Database) StopWorkspace(project, id string) (map[string]any, error) {
	out := map[string]any{}
	err := db.transaction(func() error {
		ws, e := db.lockedOne("SELECT * FROM workspaces WHERE id=? AND project=?", id, project)
		if e != nil {
			return e
		}
		if ws == nil {
			return errors.New("workspace not found")
		}
		pending, err := db.checkpointStopPendingLocked(id)
		if err != nil {
			return err
		}
		if pending {
			return errors.New("checkpoint-before-stop is pending; ordinary stop is denied")
		}
		status := asString(ws["status"])
		if status == "stopping" {
			active, err := db.lockedOne(
				"SELECT id FROM operations WHERE workspace=? AND action='stop' AND status IN ('queued','leased') ORDER BY created DESC LIMIT 1",
				id)
			if err != nil {
				return err
			}
			if active != nil {
				out = map[string]any{"status": status, "operation_id": active["id"]}
				return nil
			}
			op, err := db.EnqueueLocked(id, asString(ws["node"]), "stop", map[string]any{})
			if err != nil {
				return err
			}
			out = map[string]any{"status": "stopping", "operation_id": op}
			return db.execLocked("UPDATE workspaces SET updated=? WHERE id=?", stamp(), id)
		}
		if status == "stopped" || status == "expired" {
			out["status"] = status
			return nil
		}
		if status != "ready" && status != "failed" {
			return errors.New("workspace cannot be stopped")
		}
		if e = db.execLocked("UPDATE workspaces SET status='stopping',updated=? WHERE id=?", stamp(), id); e != nil {
			return e
		}
		if e = db.execLocked("UPDATE operations SET status='cancelled',updated=? WHERE workspace=? AND status='queued'", stamp(), id); e != nil {
			return e
		}
		op, e := db.EnqueueLocked(id, asString(ws["node"]), "stop", map[string]any{})
		out = map[string]any{"status": "stopping", "operation_id": op}
		return e
	})
	return out, err
}
