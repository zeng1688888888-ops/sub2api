package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const intelligenceSchedulingEligibleSQL = `(platform='openai' AND type IN ('oauth','setup-token')
 AND parent_account_id IS NULL
 AND COALESCE(extra->>'synthetic_ui_test','false') <> 'true'
 AND (type <> 'oauth' OR lower(btrim(COALESCE(credentials->>'auth_mode',''))) <> 'agentidentity'))`

// Manual scheduling resets streaks and invalidates in-flight probes. It never
// changes the opt-in setting saved in the add/edit account form.
func intelligenceManualSchedulingExtraSQL(extra, generation string) string {
	base := "(" + extra + ") - 'intelligence_auto_paused'"
	return "CASE WHEN " + intelligenceSchedulingEligibleSQL + " THEN (" + base + ")" +
		" || jsonb_build_object('intelligence_scheduling_generation'," + generation + "::text)" +
		" || CASE WHEN jsonb_typeof((" + extra + ")->'openai_codex_state_probe')='object'" +
		" THEN jsonb_build_object('openai_codex_state_probe',(" + extra + ")->'openai_codex_state_probe'" +
		" || jsonb_build_object('consecutive_healthy',0,'consecutive_degraded',0,'scheduling_generation'," + generation + "::text))" +
		" ELSE '{}'::jsonb END ELSE " + base + " END"
}

func (r *accountRepository) setManualSchedulable(ctx context.Context, id int64, schedulable bool) error {
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.setManualSchedulable(ctx, id, schedulable)
	}
	client := clientFromContext(ctx, r.client)
	extra := intelligenceManualSchedulingExtraSQL("COALESCE(extra,'{}'::jsonb)", "$3")
	result, err := client.ExecContext(ctx, "UPDATE accounts SET schedulable=$1, extra="+extra+
		`, updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, schedulable, id, uuid.NewString())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "enqueue schedulable change failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}
