package wishteam

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestRevivalTicketIdentity(t *testing.T) {
	require.True(t, sameTurnStateIdentity(oldFixture(), freshFixture()))
	for _, field := range []string{"chatgpt_account_id", "chatgpt_user_id", "organization_id"} {
		fresh := freshFixture()
		object(fresh["credentials"])[field] = "different"
		require.False(t, sameTurnStateIdentity(oldFixture(), fresh), field)
	}
	old, fresh := oldFixture(), freshFixture()
	delete(object(old["credentials"]), "chatgpt_account_id")
	delete(object(fresh["credentials"]), "chatgpt_account_id")
	require.False(t, sameTurnStateIdentity(old, fresh))
}

func TestPostgresRevivalInheritsTicketWithoutRenewal(t *testing.T) {
	for _, kind := range []string{"valid", "expired", "revoked", "wrong_model", "changed_user"} {
		t.Run(kind, func(t *testing.T) {
			db := testDB(t)
			for _, file := range []string{"248_codex_turn_state.sql", "249_codex_turn_state_attempts.sql", "251_codex_turn_state_transfer.sql"} {
				raw, err := migrations.FS.ReadFile(file)
				require.NoError(t, err)
				_, err = db.Exec(string(raw))
				require.NoError(t, err)
			}
			_, err := db.Exec(`CREATE TABLE settings(key text PRIMARY KEY,value text)`)
			require.NoError(t, err)
			oldID := seed(t, db, "child@example.com", 1)
			now := time.Now().UTC().Truncate(time.Second)
			expires := now.Add(25 * time.Minute)
			raw := make([]byte, 57+12*16)
			raw[0] = 0x80
			binary.BigEndian.PutUint64(raw[1:9], uint64(now.Unix()))
			state := base64.URLEncoding.EncodeToString(raw)
			model := "gpt-6-astra"
			if kind == "expired" {
				expires = now.Add(-time.Second)
			}
			if kind == "wrong_model" {
				model = "other-model"
			}
			_, err = db.Exec(`INSERT INTO codex_turn_states(account_id,state,status,model,issued_at,expires_at) VALUES($1,$2,'active',$3,$4,$5)`, oldID, state, model, now, expires)
			require.NoError(t, err)
			if kind == "revoked" {
				_, err = db.Exec(`INSERT INTO codex_turn_states(account_id,status) VALUES($1,'revoked')`, oldID)
				require.NoError(t, err)
			}
			_, err = db.Exec(`INSERT INTO codex_turn_state_attempts(account_id,attempts,next_retry_at,upstream_retry_at) VALUES($1,50,$2,$3)`, oldID, now.Add(5*time.Minute), now.Add(10*time.Minute))
			require.NoError(t, err)
			w, _, item := startFixture(t, db)
			fresh := freshFixture()
			if kind == "changed_user" {
				object(fresh["credentials"])["chatgpt_user_id"] = "another-user"
			}
			require.NoError(t, w.replace(context.Background(), item, fresh, revived(), document([]map[string]any{fresh})))
			var newID int64
			require.NoError(t, db.QueryRow(`SELECT new_account_id FROM wishteam_items WHERE id=$1`, item).Scan(&newID))
			require.NotEqual(t, oldID, newID)
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM codex_turn_states WHERE account_id=$1`, newID).Scan(&count))
			if kind != "valid" {
				require.Zero(t, count)
				return
			}
			require.Equal(t, 1, count)
			var gotState, source string
			var gotIssued, gotExpires time.Time
			require.NoError(t, db.QueryRow(`SELECT state,source,issued_at,expires_at FROM codex_turn_states WHERE account_id=$1`, newID).Scan(&gotState, &source, &gotIssued, &gotExpires))
			require.Equal(t, state, gotState)
			require.Equal(t, "revival", source)
			require.True(t, gotIssued.Equal(now))
			require.True(t, gotExpires.Equal(expires))
			var attempts int
			var cycle, upstream time.Time
			require.NoError(t, db.QueryRow(`SELECT attempts,next_retry_at,upstream_retry_at FROM codex_turn_state_attempts WHERE account_id=$1`, newID).Scan(&attempts, &cycle, &upstream))
			require.Equal(t, 50, attempts)
			require.True(t, cycle.Equal(now.Add(5*time.Minute)))
			require.True(t, upstream.Equal(now.Add(10*time.Minute)))
			require.NoError(t, w.replace(context.Background(), item, fresh, revived(), document(nil)))
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM codex_turn_states WHERE account_id=$1`, newID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
