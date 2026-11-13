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

// UpdateIntelligenceProbeSnapshot uses the current row's pause ownership, so a
// manual scheduling change made during a probe cannot be undone by stale results.
func (r *accountRepository) UpdateIntelligenceProbeSnapshot(ctx context.Context, id int64, snapshot *service.OpenAICodexStateProbeSnapshot, pause, resume bool) (service.IntelligenceSchedulingTransition, error) {
	var transition service.IntelligenceSchedulingTransition
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.UpdateIntelligenceProbeSnapshot(ctx, id, snapshot, pause, resume)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return transition, err
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
 WITH locked AS MATERIALIZED (
  SELECT id,schedulable,status,COALESCE(extra,'{}'::jsonb) AS extra
  FROM accounts WHERE id=$2 AND deleted_at IS NULL FOR UPDATE
 ), decision AS (
  SELECT *, ($3::boolean AND schedulable) AS do_pause,
   ($4::boolean AND NOT schedulable AND status='active' AND extra->>'intelligence_auto_paused'='true') IS TRUE AS do_resume
  FROM locked
 )
 UPDATE accounts a SET
  schedulable=CASE WHEN d.do_pause THEN FALSE WHEN d.do_resume THEN TRUE ELSE d.schedulable END,
  extra=(CASE WHEN d.do_pause THEN d.extra||'{"intelligence_auto_paused":true}'::jsonb
              WHEN d.do_resume THEN d.extra-'intelligence_auto_paused' ELSE d.extra END)
   ||jsonb_build_object('openai_codex_state_probe',$1::jsonb||jsonb_build_object('reason',
       COALESCE($1::jsonb->>'reason','')||CASE WHEN d.do_pause THEN $5::text WHEN d.do_resume THEN $6::text ELSE '' END)),
  updated_at=NOW()
 FROM decision d WHERE a.id=d.id
 RETURNING a.extra->'openai_codex_state_probe',d.do_pause,d.do_resume
`, string(payload), id, pause, resume, service.IntelligencePauseReason, service.IntelligenceResumeReason)
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
	if err := rows.Scan(&saved, &transition.Paused, &transition.Resumed); err != nil {
		return transition, err
	}
	if err := rows.Close(); err != nil {
		return transition, err
	}
	if err := json.Unmarshal(saved, snapshot); err != nil {
		return transition, err
	}
	if transition.Paused || transition.Resumed {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			logger.LegacyPrintf("repository.account", "enqueue intelligence scheduling change failed: account=%d err=%v", id, err)
		}
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return transition, nil
}
