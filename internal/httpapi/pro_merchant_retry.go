package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Server) reissueProMerchant(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var in struct {
		RequestID string `json:"request_id"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	if len(in.RequestID) < 8 || len(in.RequestID) > 100 {
		writeAPI(w, 400, nil, "缺少原商户申请编号，请刷新后重试")
		return
	}
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作，请等待当前操作结束")
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			lock.Unlock()
		}
	}()
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, 404, nil, "账号不存在")
		return
	}
	if !p.MerchantCanReissue() || p.ProMerchant.RequestID != in.RequestID || p.ProWorkflowRunning || p.IsProDead() {
		writeAPI(w, 409, nil, "原申请不是已确认失败的订单，或已重新提交，请刷新进度")
		return
	}
	// Read the pinned snapshot for the existing request. Do not use
	// current settings to query a previous provider's order.
	_, submission, err := s.store.ProMerchantSubmission(email, in.RequestID)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	job, err := s.merchantClient(submission).Progress(ctx, submission.APIKey, p.ProMerchant.JobID, email)
	if err != nil {
		writeAPI(w, 502, nil, "暂时无法核实原商户订单，未重新发单："+err.Error())
		return
	}
	if !job.TerminalFailure() || job.Status == "banned" || job.AccountStatus == "banned" {
		writeAPI(w, 409, nil, "商户原单当前状态："+job.Label()+"，未重新发单，请查询原单进度")
		return
	}
	if ctx.Err() != nil {
		writeAPI(w, 408, nil, "核实请求已取消，未重新发单")
		return
	}
	// A Stop issued while the remote GET was pending must win. Match the
	// existing stop/start lock order only for the short local scheduling step.
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	p, err = s.store.ReissueProMerchant(email, in.RequestID, p.ProAuto.StopVersion)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	// Persisted before scheduling. If shutdown wins here, the normal merchant
	// scheduler resumes the same new key after startup, never another new job.
	started, startErr := s.startProEndJob(email, lock.Unlock, nil, "manual_merchant")
	if startErr == nil {
		transferred = true
		p = started
	}
	writeAPI(w, 202, p, "")
}
