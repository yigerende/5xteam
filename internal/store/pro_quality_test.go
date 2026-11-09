package store

import (
	"chatgpt-space-merge/internal/model"
	"testing"
)

func TestProQualityConnectionStatusSettingsPersistAndDefault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := model.DefaultProQualitySettings()
	q.ConnectionFailureHTTPStatuses = " 502, 504,502 "
	q, err = s.SaveProQualitySettings(q)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if q.ConnectionFailureHTTPStatuses != "502,504" {
		t.Fatalf("saved: %+v", q)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.ProQualitySettings()
	if err != nil || loaded.ConnectionFailureHTTPStatuses != "502,504" {
		t.Fatalf("reload: %+v %v", loaded, err)
	}
	q.ConnectionFailureHTTPStatuses = "200,502"
	if _, err = s.SaveProQualitySettings(q); err == nil {
		t.Fatal("invalid config accepted")
	}
	loaded, _ = s.ProQualitySettings()
	if loaded.ConnectionFailureHTTPStatuses != "502,504" || loaded.Revision != q.Revision {
		t.Fatal("invalid config changed saved policy")
	}
	if _, err = s.db.Exec(`UPDATE pro_quality_settings SET payload=json_remove(payload,'$.connection_failure_http_statuses')`); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.ProQualitySettings()
	if err != nil || loaded.ConnectionFailureHTTPStatuses != "429,502,503" || loaded.Enabled {
		t.Fatalf("legacy defaults: %+v %v", loaded, err)
	}
}
