package store

import (
	"context"
	"encoding/json"
	"time"

	"chatgpt-space-merge/internal/model"
)

// These are conservative candidate filters, not permission to execute work.
// Existing workflow predicates and the per-account recheck remain authoritative.
// Partial indexes keep completed history out of frequent background reads.
const proQuotaCandidates = `json_extract(profile,'$.management_scope')='pro'
 AND json_extract(profile,'$.push_status')='completed'
 AND COALESCE(json_extract(profile,'$.pro_auto.id'),'')=''
 AND COALESCE(json_extract(profile,'$.pro_workflow_running'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_auto.stop_requested'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_auto.status'),'')<>'stopped'
 AND COALESCE(json_extract(profile,'$.pro_migration.paused'),0)=0
 AND json_extract(profile,'$.pro_merge_v2') IS NULL
 AND (COALESCE(json_extract(profile,'$.space_merged_once'),0)=0
      OR COALESCE(json_extract(profile,'$.pro_remove_status'),'')<>'completed'
      OR (json_type(profile,'$.vm2api')='object' AND json_extract(profile,'$.vm2api.deleted_at') IS NULL))`

const proForceCandidates = `json_extract(profile,'$.management_scope')='pro'
 AND json_extract(profile,'$.push_status')='completed'
 AND COALESCE(json_extract(profile,'$.pro_workflow_running'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_auto.stop_requested'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_auto.status'),'')<>'stopped'
 AND COALESCE(json_extract(profile,'$.pro_migration.paused'),0)=0
 AND json_extract(profile,'$.pro_merge_v2') IS NULL
 AND NOT (COALESCE(json_extract(profile,'$.pro_transfer_status'),'')='completed' AND COALESCE(json_extract(profile,'$.pro_remove_status'),'')='completed')
 AND (json_extract(profile,'$.pro_push_timeout_enabled')=1 OR json_type(profile,'$.pro_daily_merge')='object')`

const proSIWCCandidates = `json_extract(profile,'$.management_scope')='pro' AND json_type(profile,'$.pro_siwc')='object'`
const proQualityCandidates = `json_extract(profile,'$.management_scope')='pro'
 AND (json_type(profile,'$.pro_siwc')='object' OR (json_extract(profile,'$.push_status')='completed' AND json_extract(profile,'$.push_provider')='sub2'
 AND json_extract(profile,'$.sub2_account_id')>0 AND json_extract(profile,'$.vm2api') IS NULL
 AND COALESCE(json_extract(profile,'$.pro_remove_status'),'') NOT IN ('completed','team_removed')
 AND json_extract(profile,'$.pro_merge_v2') IS NULL))`

const proSupplyCandidates = `json_extract(profile,'$.management_scope')='pro'
 AND json_extract(profile,'$.push_status')='completed' AND json_extract(profile,'$.push_provider')='sub2'
 AND json_extract(profile,'$.sub2_account_id')>0
 AND (json_type(profile,'$.pro_dead')='object' OR
 (COALESCE(json_extract(profile,'$.space_merged_once'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_remove_status'),'') NOT IN ('completed','team_removed')
 AND json_extract(profile,'$.pro_merge_v2') IS NULL))`

func (s *Store) ProSupplyProfiles(ctx context.Context) ([]model.MailAccountProfile, error) {
	return s.backgroundProProfiles(ctx, proSupplyCandidates)
}

func (s *Store) ProSub2Profiles(ctx context.Context, accountID int64) ([]model.MailAccountProfile, error) {
	return s.backgroundProProfiles(ctx, "json_extract(profile,'$.management_scope')='pro' AND json_extract(profile,'$.sub2_account_id')=?", accountID)
}

func (s *Store) ProUnpushedProfiles(ctx context.Context) ([]model.MailAccountProfile, error) {
	return s.backgroundProProfiles(ctx, "json_extract(profile,'$.management_scope')='pro' AND COALESCE(json_extract(profile,'$.push_status'),'')<>'completed'")
}

func (s *Store) initializeProMonitorIndexes() error {
	for name, predicate := range map[string]string{
		"mail_pro_supply_candidates": proSupplyCandidates,
		"mail_pro_quota_candidates":  proQuotaCandidates, "mail_pro_force_candidates": proForceCandidates,
		"mail_pro_siwc_candidates": proSIWCCandidates, "mail_pro_quality_candidates": proQualityCandidates,
	} {
		if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS " + name + " ON mail_accounts(email) WHERE " + predicate); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backgroundProProfiles(ctx context.Context, predicate string, args ...any) ([]model.MailAccountProfile, error) {
	reader, err := s.ProListReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.proRestockProfiles(ctx, "SELECT profile,updated_at FROM mail_accounts WHERE "+predicate, args...)
}

func (s *Store) ProQuotaCandidates(ctx context.Context) ([]model.MailAccountProfile, error) {
	return s.backgroundProProfiles(ctx, proQuotaCandidates)
}

func (s *Store) ProForceMergeCandidates(ctx context.Context, now time.Time) ([]model.MailAccountProfile, error) {
	// Include daily registrations even if their ordinary push deadline is absent.
	return s.backgroundProProfiles(ctx, proForceCandidates+` AND (
 json_type(profile,'$.pro_daily_merge')='object' OR
 julianday(json_extract(profile,'$.pro_push_started_at')) + json_extract(profile,'$.pro_push_timeout_minutes')/1440.0 <= julianday(?))`, formatTime(now))
}

func (s *Store) ProSIWCCandidates(ctx context.Context, transferringOnly bool) ([]model.MailAccountProfile, error) {
	where := proSIWCCandidates
	if transferringOnly {
		where += ` AND COALESCE(json_extract(profile,'$.pro_siwc.status'),'') NOT IN ('active','ended') AND COALESCE(json_extract(profile,'$.space_merged_once'),0)=0`
	}
	return s.backgroundProProfiles(ctx, where)
}

func (s *Store) ProQualityCandidates(ctx context.Context) ([]model.MailAccountProfile, error) {
	return s.backgroundProProfiles(ctx, proQualityCandidates)
}

// The merge-page picker needs availability flags, not credentials, workflows or
// financial history. Selected tokens still use the existing authenticated API.
type ProAccountOption struct {
	Email              string     `json:"email"`
	AccessTokenPresent bool       `json:"access_token_present"`
	ChatGPTStatus      string     `json:"chatgpt_status"`
	ATValid            bool       `json:"at_valid"`
	ATCheckedAt        *time.Time `json:"at_checked_at,omitempty"`
}

func (s *Store) ProAccountOptions(ctx context.Context) ([]ProAccountOption, error) {
	reader, err := s.ProListReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	rows, err := reader.db.QueryContext(ctx, `SELECT json_object('email',email,
 'access_token_present',json(CASE WHEN json_extract(profile,'$.access_token_present')=1 THEN 'true' ELSE 'false' END),
 'chatgpt_status',COALESCE(json_extract(profile,'$.chatgpt_status'),''),
 'at_valid',json(CASE WHEN json_extract(profile,'$.at_valid')=1 THEN 'true' ELSE 'false' END),
 'at_checked_at',json_extract(profile,'$.at_checked_at'))
 FROM mail_accounts WHERE json_extract(profile,'$.management_scope')='pro'
 ORDER BY COALESCE(json_extract(profile,'$.created_at'),updated_at) DESC,email COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProAccountOption{}
	for rows.Next() {
		var raw string
		var item ProAccountOption
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
