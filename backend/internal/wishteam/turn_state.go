package wishteam

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func sameTurnStateIdentity(old, fresh map[string]any) bool {
	a, b := object(old["credentials"]), object(fresh["credentials"])
	// An email match alone does not prove that the workspace stayed the same.
	if emailOf(old) == "" || emailOf(old) != emailOf(fresh) {
		return false
	}
	if strings.TrimSpace(text(a["chatgpt_account_id"])) == "" || text(a["chatgpt_account_id"]) != text(b["chatgpt_account_id"]) {
		return false
	}
	for _, key := range []string{"chatgpt_user_id", "organization_id"} {
		if text(a[key]) != text(b[key]) {
			return false
		}
	}
	return true
}

// Called inside the account replacement transaction. No timestamp is renewed,
// and history remains associated with the old account for auditing.
func inheritTurnState(ctx context.Context, tx *sql.Tx, oldID, newID int64, old, fresh map[string]any) error {
	if !sameTurnStateIdentity(old, fresh) {
		return nil
	}
	var installed bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('codex_turn_state_revivals') IS NOT NULL`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyCodexTurnStateConfig).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	cfg := service.CodexTurnStateConfig{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	cfg = service.NormalizeCodexTurnStateConfig(cfg)
	var pinID int64
	var state, model string
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT id,state,model,expires_at FROM codex_turn_states s
WHERE account_id=$1 AND status='active' AND expires_at>NOW() AND length(state) IN(292,332)
AND id>COALESCE((SELECT MAX(id) FROM codex_turn_states WHERE account_id=$1 AND status='revoked'),0)
ORDER BY id DESC LIMIT 1`, oldID).Scan(&pinID, &state, &model, &expires)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var inherited int64
	if err == nil && cfg.TicketQualified(state, model, expires, false, time.Now()) {
		err = tx.QueryRowContext(ctx, `INSERT INTO codex_turn_states(account_id,group_id,state,state_fingerprint,status,source,http_status,model,proxy_id,latency_ms,issued_at,expires_at,error,created_at)
SELECT $1,group_id,state,state_fingerprint,status,'revival',http_status,model,proxy_id,latency_ms,issued_at,expires_at,'',created_at
FROM codex_turn_states WHERE id=$2 RETURNING id`, newID, pinID).Scan(&inherited)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_turn_state_attempts(account_id,attempts,updated_at,next_retry_at,upstream_retry_at)
SELECT $1,attempts,updated_at,next_retry_at,upstream_retry_at FROM codex_turn_state_attempts WHERE account_id=$2
ON CONFLICT(account_id) DO NOTHING`, newID, oldID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_turn_state_revivals(old_account_id,new_account_id,ticket_record_id) VALUES($1,$2,$3) ON CONFLICT(old_account_id) DO NOTHING`, oldID, newID, inherited); err != nil {
		return err
	}
	return nil
}
