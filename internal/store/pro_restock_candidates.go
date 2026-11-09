package store

import (
	"context"

	"chatgpt-space-merge/internal/model"
)

// Restrict external reconciliation to untracked work before decoding profiles.
// Completed deliveries and idle imported accounts are history, not incoming
// supply. Keep missing explicit payment links and non-successful linked orders
// for the lifecycle check; never infer settlement from a completed UI stage.
// The fallback key mirrors ProAutoState.PaymentOrderID, including retry keys.
const proRestockCandidatesQuery = `
SELECT a.profile,a.updated_at FROM mail_accounts AS a
LEFT JOIN gptpay_orders AS payment ON payment.id=coalesce(
 nullif(json_extract(a.profile,'$.pro_auto.order_id'),''),
 CASE WHEN coalesce(json_extract(a.profile,'$.pro_auto.id'),'')!='' THEN
  'pro-auto-' || json_extract(a.profile,'$.pro_auto.id') ||
  CASE WHEN coalesce(json_extract(a.profile,'$.pro_auto.retry_attempts'),0)>0
   THEN '-retry-' || json_extract(a.profile,'$.pro_auto.retry_attempts') ELSE '' END
 END)
WHERE json_extract(a.profile,'$.management_scope')='pro'
 AND coalesce(json_extract(a.profile,'$.pro_restock_task_id'),'')=''
 AND (
  EXISTS(SELECT 1 FROM gptpay_orders AS active WHERE active.email=a.email AND active.active=1)
  OR coalesce(json_type(a.profile,'$.pro_auto'),'null') NOT IN ('null','object')
  OR (coalesce(json_extract(a.profile,'$.pro_auto.id'),'')!='' AND (
   coalesce(json_extract(a.profile,'$.pro_auto.status'),'')!='completed'
   OR (coalesce(json_extract(payment.profile,'$.status'),'')!='success' AND (
    coalesce(json_extract(a.profile,'$.pro_auto.order_id'),'')!='' OR payment.id IS NOT NULL
   ))
  ))
  OR EXISTS(SELECT 1 FROM json_each(a.profile,'$.pro_manual_stages') AS stage
   WHERE json_extract(stage.value,'$.status')='running')
  OR (coalesce(json_extract(a.profile,'$.push_status'),'')!='completed'
   AND json_extract(a.profile,'$.pro_manual_stages.recharge.status')='completed')
 )`

func (s *Store) ProRestockExternalProfiles(ctx context.Context) ([]model.MailAccountProfile, error) {
	return s.proRestockProfiles(ctx, proRestockCandidatesQuery)
}
