package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// These are the saved inputs of readEJHDashboard and its card/case projections.
// Triggers also cover external connections, deletion and equal-timestamp edits.
// Unrelated audit/history/restock jobs must not rebuild the whole card dashboard.
var ejhDisplayTables = []string{
	"ejh_records", "ejh_cases", "ejh_return_links", "ejh_reversal_fee_rates",
	"ejh_case_states", "ejh_received_returns", "ejh_card_links", "ejh_operation_claims", "ejh_settings",
	"gptpay_cards", "gptpay_orders", "gptpay_settings", "mail_accounts", "pro_oai_confirmation_accounts",
	"pro_card_usage", "pro_card_reservations", "pro_card_identities", "pro_card_types", "pro_card_limits",
	"pro_card_order_releases", "pro_dead_cards", "pro_dead_accounts", "pro_merchant_fees", "card_payment_modes",
	"nexovcc_cards", "nexovcc_settings", "nexovcc_operations",
}

func (s *Store) initializeEJHDisplayRevision() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS display_revisions(scope TEXT PRIMARY KEY, revision INTEGER NOT NULL);
	INSERT OR IGNORE INTO display_revisions VALUES('ejh',1);`); err != nil {
		return err
	}
	for _, table := range ejhDisplayTables {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			// Do not exclude unknown record kinds: future financial additions must
			// invalidate by default. These known records are never dashboard inputs.
			when := ""
			if table == "mail_accounts" && event == "UPDATE" {
				// Quota checks and login logs update the same account profile, but
				// only OAI action prerequisites and saved revenue affect this view.
				old := strings.ReplaceAll(ejhCaseAccountProjection, "m.profile", "OLD.profile")
				next := strings.ReplaceAll(ejhCaseAccountProjection, "m.profile", "NEW.profile")
				when = " WHEN OLD.email IS NOT NEW.email OR " + old + " IS NOT " + next +
					` OR json_extract(OLD.profile,'$.user_cost_downstream_identity','$.user_cost_downstream_snapshot') IS NOT json_extract(NEW.profile,'$.user_cost_downstream_identity','$.user_cost_downstream_snapshot')`
			}
			if table == "ejh_records" {
				predicate := func(row string) string {
					return row + ".kind NOT IN ('match_audit','finance_snapshot','finance_usage_day','finance_refund_fee_audit')"
				}
				switch event {
				case "INSERT":
					when = predicate("NEW")
				case "DELETE":
					when = predicate("OLD")
				default:
					when = "(" + predicate("OLD") + " OR " + predicate("NEW") + ")"
				}
				when = " WHEN " + when
			}
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS ejh_display_%s_%s AFTER %s ON %s%s BEGIN
			UPDATE display_revisions SET revision=revision+1 WHERE scope='ejh'; END;`, table, strings.ToLower(event), event, table, when)
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	// A separate trigger also upgrades databases with the original IF NOT EXISTS
	// account trigger. Only a change in eligibility invalidates this projection.
	old := strings.ReplaceAll(ejhConsumptionNotPushedProjection, "m.profile", "OLD.profile")
	next := strings.ReplaceAll(ejhConsumptionNotPushedProjection, "m.profile", "NEW.profile")
	if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS ejh_display_mail_consumption_update AFTER UPDATE ON mail_accounts
	WHEN ` + old + ` IS NOT ` + next + ` BEGIN
	UPDATE display_revisions SET revision=revision+1 WHERE scope='ejh'; END;`); err != nil {
		return err
	}
	old = strings.ReplaceAll(ejhConsumptionReviewProjection, "m.profile", "OLD.profile")
	next = strings.ReplaceAll(ejhConsumptionReviewProjection, "m.profile", "NEW.profile")
	oldEligible := strings.ReplaceAll(ejhConsumptionNoSourceProjection, "m.profile", "OLD.profile")
	newEligible := strings.ReplaceAll(ejhConsumptionNoSourceProjection, "m.profile", "NEW.profile")
	if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS ejh_display_mail_consumption_review_update AFTER UPDATE ON mail_accounts
	WHEN ` + old + ` IS NOT ` + next + ` OR ` + oldEligible + ` IS NOT ` + newEligible + ` BEGIN
	UPDATE display_revisions SET revision=revision+1 WHERE scope='ejh'; END;`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EJHDisplayRevision(ctx context.Context) (string, error) {
	// Initialize/recover the persistent read-only connection using the existing
	// identity. Its global data_version is deliberately not the cache key here.
	s.revisionMu.Lock()
	defer s.revisionMu.Unlock()
	if _, err := s.readRevisionLocked(ctx); err != nil {
		return "", err
	}
	var version int64
	if err := s.revisionDB.QueryRowContext(ctx, `SELECT revision FROM display_revisions WHERE scope='ejh'`).Scan(&version); err != nil {
		return "", err
	}
	// Card expiry and date-sensitive display fields also advance without writes.
	return fmt.Sprintf("%s:%d:ejh:%d:%d", s.revisionIdentity, s.revisionEpoch, version, time.Now().Unix()/60), nil
}
