package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"net/http"
)

func (s *Server) ensureEJHSigningKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PrivateKey string `json:"private_key"`
		Regenerate bool   `json:"regenerate"`
	}
	if decodeJSON(w, r, &in, 24<<10) != nil {
		return
	}
	if !s.ejh.mu.TryLock() {
		writeAPI(w, 409, nil, "卡台操作正在执行，请稍后生成签名密钥")
		return
	}
	defer s.ejh.mu.Unlock()
	if in.Regenerate && in.PrivateKey != "" {
		writeAPI(w, 400, nil, "重新生成与导入私钥不能同时操作")
		return
	}
	var cfg ejh.Settings
	var err error
	if in.Regenerate {
		cfg, err = s.store.RegenerateEJHSigningKey()
	} else {
		cfg, err = s.store.EnsureEJHSigningKey(in.PrivateKey)
	}
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, 200, cfg, "")
}
