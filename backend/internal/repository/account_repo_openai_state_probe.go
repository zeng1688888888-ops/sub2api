package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListDueOpenAICodexStateProbeAccounts keeps the periodic monitor bounded at
// the database layer. It only returns active OpenAI OAuth-like accounts; the
// service still applies the final account capability checks before probing.
func (r *accountRepository) ListDueOpenAICodexStateProbeAccounts(ctx context.Context, now time.Time, limit int) ([]service.Account, error) {
	return r.listDueAccountAutomaticProbes(ctx, now, limit, service.OpenAICodexStateProbeSnapshotExtraKey, "last_probe_at")
}

func (r *accountRepository) ListDueAccountSVGProbeAccounts(ctx context.Context, now time.Time, limit int) ([]service.Account, error) {
	return r.listDueAccountAutomaticProbes(ctx, now, limit, service.AccountSVGProbeExtraKey, "finished_at")
}

func (r *accountRepository) listDueAccountAutomaticProbes(ctx context.Context, now time.Time, limit int, extraKey, timeField string) ([]service.Account, error) {
	if limit <= 0 {
		return []service.Account{}, nil
	}
	if r.sql == nil {
		return nil, errors.New("account repository SQL executor not configured")
	}

	rows, err := r.sql.QueryContext(ctx, `
		WITH candidates AS (
			SELECT
				id,
				CASE WHEN $3::text = 'openai_codex_state_probe'
					AND (extra #>> '{openai_codex_state_probe,method}') IS DISTINCT FROM 'candy' THEN NULL
					ELSE extra -> $3::text ->> $4::text END AS last_probe_at
			FROM accounts
			WHERE deleted_at IS NULL
				AND status = 'active'
				AND platform = 'openai'
				AND type IN ('oauth', 'setup-token')
				AND parent_account_id IS NULL
				AND COALESCE(extra ->> 'synthetic_ui_test', 'false') <> 'true'
		), parsed AS MATERIALIZED (
			SELECT
				id,
				last_probe_at,
				jsonb_path_query_first(
					jsonb_build_object(
						'value', replace(regexp_replace(regexp_replace(
							last_probe_at,
							'(\.[0-9]{6})[0-9]+(Z|[+-][0-9]{2}:[0-9]{2})$',
							'\1\2'
						), 'Z$', '+00:00'), 'T', ' ')
					),
					'$.value.datetime()',
					'{}'::jsonb,
					true
				) #>> '{}' AS parsed_last_probe_at
			FROM candidates
		)
		SELECT id
		FROM parsed
		WHERE last_probe_at IS NULL
			OR parsed_last_probe_at IS NULL
			OR parsed_last_probe_at::timestamptz <= $1
		ORDER BY parsed_last_probe_at::timestamptz NULLS FIRST, id
		LIMIT $2
	`, now.UTC().Add(-service.AccountAutomaticProbeInterval), limit, extraKey, timeField)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []service.Account{}, nil
	}

	accounts, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			result = append(result, *account)
		}
	}
	return result, nil
}

// UpdateIntelligenceProbeSnapshot atomically records the verdict and disables scheduling.
// A healthy/inconclusive verdict never re-enables an account paused by an operator.
func (r *accountRepository) UpdateIntelligenceProbeSnapshot(ctx context.Context, id int64, snapshot *service.OpenAICodexStateProbeSnapshot, pause bool) error {
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.UpdateIntelligenceProbeSnapshot(ctx, id, snapshot, pause)
	}
	payload, err := json.Marshal(map[string]any{service.OpenAICodexStateProbeSnapshotExtraKey: snapshot})
	if err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)||$1::jsonb,
 schedulable=CASE WHEN $3 THEN FALSE ELSE schedulable END, updated_at=NOW()
 WHERE id=$2 AND deleted_at IS NULL`, string(payload), id, pause)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrAccountNotFound
	}
	if pause {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			logger.LegacyPrintf("repository.account", "enqueue intelligence pause failed: account=%d err=%v", id, err)
		}
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}
