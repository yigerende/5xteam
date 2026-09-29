package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func (s *Server) applySeatRecoveryControl(id, action string) (model.SeatRecoveryTask, int, error) {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	p, err := s.store.SeatRecoveryTask(id)
	if err != nil {
		return p, 404, errors.New("任务不存在")
	}
	switch action {
	case "pause", "resume", "retry", "cancel", "delete", "mother_kick", "child_leave":
	default:
		return p, 400, errors.New("操作无效")
	}
	if action == "delete" {
		if err = s.store.DeleteSeatRecoveryTask(id); err != nil {
			return p, 500, err
		}
		if cancel := s.recovery.taskCancels[id]; cancel != nil {
			cancel()
		}
		// Keep runtime/account/mother locks until the cancelled worker unwinds.
		// Deletion itself never waits for a network request or sends a removal.
		return p, 200, nil
	}
	if p.Finished {
		return p, 409, errors.New("任务已结束")
	}
	manual := action == "mother_kick" || action == "child_leave"
	if manual && (p.PendingAction != "" || ((p.Stage == "manual_remove" || p.Stage == "manual_cooldown") && p.Status != "failed")) {
		return p, 409, errors.New("手动移出已在排队或执行，请等待结果")
	}
	if action == "retry" && (p.Status != "failed" || s.recovery.active[id]) {
		return p, 409, errors.New("仅可重试已停止的失败任务")
	}
	if action == "cancel" && (p.Stage != "login" || s.recovery.active[id] || p.PendingAction != "") {
		return p, 409, errors.New("仅未开始进入请求的空任务可以取消")
	}
	p, err = s.store.UpdateSeatRecoveryTask(id, func(v *model.SeatRecoveryTask) {
		switch action {
		case "pause":
			v.Paused = true
		case "resume":
			v.Paused = false
		case "retry":
			v.Paused = false
			v.Status, v.Message = "queued", "等待核实远端状态后续跑"
			v.NextAt = time.Now()
			v.Checks, v.RetryMutation = 0, true
		case "cancel":
			v.Finished, v.Status, v.Message = true, "cancelled", "已取消，未执行进入空间"
		case "mother_kick", "child_leave":
			v.PendingAction = action
			v.Paused = false
			v.Status = "queued"
			v.NextAt = time.Now()
			v.Message = "手动移出已排队；当前请求结束后核实成员并执行"
		}
	})
	if err != nil {
		return p, 500, err
	}
	s.recoveryLog(p, "操作："+action)
	return p, 200, nil
}

type recoveryBatchInput struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

func recoveryUniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) batchControlSeatRecovery(w http.ResponseWriter, r *http.Request) {
	var input recoveryBatchInput
	if decodeJSON(w, r, &input, 1<<20) != nil {
		return
	}
	if len(input.IDs) == 0 || len(input.IDs) > 1000 {
		writeAPI(w, 400, nil, "请选择 1–1000 个任务")
		return
	}
	results := []map[string]any{}
	for _, id := range recoveryUniqueIDs(input.IDs) {
		p, status, err := s.applySeatRecoveryControl(id, input.Action)
		item := map[string]any{"id": id, "email": p.Email, "ok": err == nil, "status_code": status}
		if err != nil {
			item["error"] = err.Error()
		} else {
			item["task"] = p
		}
		results = append(results, item)
	}
	writeAPI(w, 200, map[string]any{"results": results}, "")
}

func (s *Server) statusSeatRecoveryTasks(w http.ResponseWriter, r *http.Request) {
	var input recoveryBatchInput
	if decodeJSON(w, r, &input, 1<<20) != nil {
		return
	}
	if len(input.IDs) > 1000 {
		writeAPI(w, 400, nil, "最多查询 1000 个任务")
		return
	}
	items := []model.SeatRecoveryTask{}
	for _, id := range recoveryUniqueIDs(input.IDs) {
		if p, err := s.store.SeatRecoveryTask(id); err == nil {
			items = append(items, p)
		}
	}
	writeAPI(w, 200, map[string]any{"items": items}, "")
}
