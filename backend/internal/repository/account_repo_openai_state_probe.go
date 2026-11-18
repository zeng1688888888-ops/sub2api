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
// the database layer. Quota/cooldown checks run before the output limit; the
// service rechecks the latest account immediately before executing a probe.
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

	result := make([]service.Account, 0, limit)
	// Page through due accounts without letting exhausted rows consume the limit.
	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		ids, err := r.listDueAccountAutomaticProbeIDs(ctx, now, pageSize, offset, extraKey, timeField)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return result, nil
		}
		accounts, err := r.GetByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]*service.Account, len(accounts))
		for _, account := range accounts {
			if account != nil {
				byID[account.ID] = account
			}
		}
		for _, id := range ids {
			if account := byID[id]; service.AccountAutomaticProbeAvailable(account, now) {
				result = append(result, *account)
				if len(result) == limit {
					return result, nil
				}
			}
		}
		if len(ids) < pageSize {
			return result, nil
		}
	}
}

func (r *accountRepository) listDueAccountAutomaticProbeIDs(ctx context.Context, now time.Time, limit, offset int, extraKey, timeField string) ([]int64, error) {
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
				AND (type <> 'oauth' OR lower(btrim(COALESCE(credentials->>'auth_mode',''))) <> 'agentidentity')
				AND (rate_limit_reset_at IS NULL OR rate_limit_reset_at <= $6)
				AND (overload_until IS NULL OR overload_until <= $6)
				AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until <= $6)
				AND (NOT auto_pause_on_expired OR expires_at IS NULL OR expires_at > $6)
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
		LIMIT $2 OFFSET $5
	`, now.UTC().Add(-service.AccountAutomaticProbeInterval), limit, extraKey, timeField, offset, now.UTC())
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
	return ids, nil
}

// UpdateIntelligenceProbeSnapshot checks the locked row's mode and generation.
// A probe started before a manual scheduling change still records its answer,
// but cannot pause the account or restore the old consecutive-failure count.
func (r *accountRepository) UpdateIntelligenceProbeSnapshot(ctx context.Context, id int64, snapshot *service.OpenAICodexStateProbeSnapshot, pause bool) (service.IntelligenceSchedulingTransition, error) {
	var transition service.IntelligenceSchedulingTransition
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.UpdateIntelligenceProbeSnapshot(ctx, id, snapshot, pause)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return transition, err
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
 WITH locked AS MATERIALIZED (
  SELECT id,schedulable,COALESCE(extra,'{}'::jsonb) AS extra,
   `+intelligenceSchedulingEligibleSQL+` AS eligible
  FROM accounts WHERE id=$2 AND deleted_at IS NULL FOR UPDATE
 ), generation AS (
  SELECT *, COALESCE(extra->>'intelligence_scheduling_generation','') = COALESCE($1::jsonb->>'scheduling_generation','') AS same_generation
  FROM locked
 ), decision AS (
  SELECT *, ($3::boolean AND eligible AND schedulable AND same_generation
   AND COALESCE(extra->'intelligence_auto_pause_enabled','false'::jsonb) = 'true'::jsonb) AS do_pause
  FROM generation
 )
 UPDATE accounts a SET
  schedulable=CASE WHEN d.do_pause THEN FALSE ELSE d.schedulable END,
  extra=(CASE WHEN d.do_pause THEN d.extra||'{"intelligence_auto_paused":true}'::jsonb ELSE d.extra END)
   ||jsonb_build_object('openai_codex_state_probe', $1::jsonb
    ||CASE WHEN d.same_generation THEN '{}'::jsonb ELSE jsonb_build_object(
      'consecutive_healthy',0,'consecutive_degraded',0,
      'scheduling_generation',COALESCE(d.extra->>'intelligence_scheduling_generation','')) END
    ||jsonb_build_object('reason', COALESCE($1::jsonb->>'reason','')||CASE WHEN d.do_pause THEN $4::text ELSE '' END)),
  updated_at=NOW()
 FROM decision d WHERE a.id=d.id
 RETURNING a.extra->'openai_codex_state_probe',d.do_pause
`, string(payload), id, pause, service.IntelligencePauseReason)
	if err != nil {
		return transition, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return transition, err
		}
		return transition, service.ErrAccountNotFound
	}
	var saved []byte
	if err := rows.Scan(&saved, &transition.Paused); err != nil {
		return transition, err
	}
	if err := rows.Close(); err != nil {
		return transition, err
	}
	if err := json.Unmarshal(saved, snapshot); err != nil {
		return transition, err
	}
	if transition.Paused {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			logger.LegacyPrintf("repository.account", "enqueue intelligence scheduling change failed: account=%d err=%v", id, err)
		}
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return transition, nil
}
