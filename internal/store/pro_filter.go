package store

import "strings"

// Listing, counts and filtered export share the same partition. Defaults such
// as an empty ProAuto object or pending merge steps do not mean work started.
// Include persisted manual/legacy progress, matching the stages shown by the UI.
// The payment lookup uses the existing email index and never reads credentials.
const proAccountsCTE = `WITH accounts AS (
	SELECT email, profile, encrypted_credentials, updated_at,
		CASE WHEN json_extract(profile,'$.space_merged_once')=1 THEN 'merged'
		WHEN COALESCE(json_extract(profile,'$.pro_auto.id'),'')<>''
			OR COALESCE(json_extract(profile,'$.pro_auto.status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.pro_auto.stage'),'')<>''
			OR EXISTS (SELECT 1 FROM json_each(profile,'$.pro_auto.steps') WHERE COALESCE(value,'') NOT IN ('','not_started','pending'))
			OR EXISTS (SELECT 1 FROM json_each(profile,'$.pro_manual_stages') WHERE COALESCE(json_extract(value,'$.status'),'') NOT IN ('','not_started','pending'))
			OR json_extract(profile,'$.pro_workflow_running')=1
			OR COALESCE(json_extract(profile,'$.pro_invite_status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.pro_accept_status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.pro_transfer_status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.pro_remove_status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.push_status'),'') NOT IN ('','not_started','pending')
			OR COALESCE(json_extract(profile,'$.quota_status'),'') NOT IN ('','not_started','pending')
			OR (json_extract(profile,'$.chatgpt_session_present')=1 AND json_extract(profile,'$.access_token_present')=1)
			OR (COALESCE(json_extract(profile,'$.oauth_authorized_at'),'')<>'' AND json_extract(profile,'$.access_token_present')=1 AND json_extract(profile,'$.refresh_token_present')=1)
			OR EXISTS (SELECT 1 FROM gptpay_orders o WHERE o.email=mail_accounts.email)
		THEN 'in_progress' ELSE 'unmerged' END AS merge_state,
		LOWER(email || ' ' || COALESCE(json_extract(profile,'$.current_plan_type'),'') || ' ' || COALESCE(json_extract(profile,'$.push_provider'),'')) AS search_text,
		COALESCE(NULLIF(json_extract(profile,'$.pro_managed_at'),''), NULLIF(json_extract(profile,'$.updated_at'),''), updated_at) AS managed_at
	FROM mail_accounts WHERE LOWER(COALESCE(json_extract(profile,'$.management_scope'),''))='pro'
)`

func normalizeProMergeFilter(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "unmerged", "in_progress", "merged":
		return value
	default:
		return ""
	}
}
