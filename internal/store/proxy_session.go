package store

import (
	"chatgpt-space-merge/internal/model"
	"database/sql"
)

func saveProxySession(tx *sql.Tx, p model.ProxyProfile) error {
	_, err := tx.Exec(`INSERT INTO proxy_sessions(proxy_id,vendor,country,session_minutes) VALUES(?,?,?,?) ON CONFLICT(proxy_id) DO UPDATE SET vendor=excluded.vendor,country=excluded.country,session_minutes=excluded.session_minutes`, p.ID, p.Vendor, p.Country, p.SessionMinutes)
	return err
}
