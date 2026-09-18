package wishteam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdatedAPIHistoricalReplacement(t *testing.T) {
	var verdict RemoteItem
	require.NoError(t, json.Unmarshal([]byte(`{"email":"child@example.com","status":"alive","reused_current":true}`), &verdict))
	out, err := replacement(oldFixture(), freshFixture(), verdict)
	require.NoError(t, err)
	require.Equal(t, "new-at", object(out["credentials"])["access_token"])
	require.Equal(t, true, out["schedulable"])
	// A historical replacement flag does not override explicit uncertainty.
	verdict.Probe = Probe{Reason: "probe_unavailable"}
	_, err = replacement(oldFixture(), freshFixture(), verdict)
	require.Error(t, err)
	// A successful NEW-token probe is not evidence against the original token.
	require.NoError(t, json.Unmarshal([]byte(`{"email":"child@example.com","status":"revived","probe":{},"replacement_probe":{"http_status":401}}`), &verdict))
	_, err = replacement(oldFixture(), freshFixture(), verdict)
	require.Error(t, err)
}

func TestUpdatedAPIProtocolAndMetadata(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		require.Equal(t, "no-store", r.Header.Get("Cache-Control"))
		require.Equal(t, "no-cache", r.Header.Get("Pragma"))
		if r.Method == "POST" {
			var body struct {
				Sub2 Document `json:"sub2"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Len(t, body.Sub2.Accounts, 1)
			fmt.Fprint(w, `{"success":true,"task_id":"fixture-task-123","task":{"status":"queued"}}`)
		} else {
			fmt.Fprint(w, `{"success":true,"output_mode":"repaired_only","partial":true,"results":[{"email":"child@example.com","status":"alive","reused_current":true,"probe":{"http_status":401,"provider":"chixiaotao","provider_http_status":502},"replacement_probe":{"http_status":200,"plan_type":"team"}}],"sub2":{"type":"sub2api-data","version":1,"accounts":[]}}`)
		}
	}))
	defer server.Close()
	c := newClient()
	c.base = server.URL
	ctx := context.Background()
	reply, err := c.submit(ctx, document([]map[string]any{outbound(oldFixture())}))
	require.NoError(t, err)
	_, err = c.poll(ctx, reply.TaskID, false)
	require.NoError(t, err)
	result, err := c.poll(ctx, reply.TaskID, true)
	require.NoError(t, err)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	for _, field := range []string{`"output_mode":"repaired_only"`, `"partial":true`, `"replacement_probe"`, `"provider":"chixiaotao"`, `"provider_http_status":502`} {
		require.Contains(t, string(raw), field)
	}
	require.Equal(t, []string{
		"POST /api/revive/batch/jobs",
		"GET /api/revive/batch/jobs/fixture-task-123",
		"GET /api/revive/batch/jobs/fixture-task-123/result",
	}, paths)
	var p Probe
	require.NoError(t, json.Unmarshal([]byte(`{"provider":"arbitrary-secret","provider_http_status":999,"http_status":401}`), &p))
	raw, err = json.Marshal(safeProbe(p))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "arbitrary-secret")
	require.NotContains(t, string(raw), "999")
}

func TestUpdatedAPIPostgresResults(t *testing.T) {
	for _, scenario := range []string{"healthy_empty", "historical_replacement", "partial_then_complete", "interrupted", "wrong_mode"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			oldID := seed(t, db, "child@example.com", 1)
			w, _, _ := startFixture(t, db)
			posts, downloads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					fmt.Fprint(out, `{"success":true,"task_id":"fixture-task-123"}`)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/result") {
					state := "done"
					if scenario == "interrupted" {
						state = "interrupted"
					}
					fmt.Fprintf(out, `{"success":true,"task":{"status":%q,"items":[]}}`, state)
					return
				}
				downloads++
				mode, partial := "repaired_only", false
				if scenario == "wrong_mode" {
					mode = "current"
				}
				if scenario == "partial_then_complete" && downloads == 1 {
					partial = true
				}
				v := RemoteItem{Email: "child@example.com", Status: "alive"}
				accounts := []map[string]any{}
				if scenario != "healthy_empty" {
					v.ReusedCurrent = true
					accounts = append(accounts, freshFixture())
				}
				require.NoError(t, json.NewEncoder(out).Encode(map[string]any{
					"success": true, "output_mode": mode, "partial": partial,
					"results": []RemoteItem{v}, "sub2": document(accounts),
				}))
			}))
			defer server.Close()
			w.client.base = server.URL
			ctx := context.Background()
			require.NoError(t, w.step(ctx)) // prepare
			require.NoError(t, w.step(ctx)) // submit
			_, err := db.Exec(`UPDATE wishteam_batches SET next_poll_at=NOW()`)
			require.NoError(t, err)
			require.NoError(t, w.step(ctx)) // poll/download
			if scenario == "partial_then_complete" {
				var state string
				require.NoError(t, db.QueryRow(`SELECT status FROM wishteam_batches`).Scan(&state))
				require.Equal(t, "polling", state)
				var count int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_archives`).Scan(&count))
				require.Zero(t, count)
				// Resume without resubmitting, including across process restart.
				w = New(db)
				w.client.base = server.URL
				_, err = db.Exec(`UPDATE wishteam_batches SET next_poll_at=NOW()`)
				require.NoError(t, err)
				require.NoError(t, w.step(ctx))
			}
			for n := 0; n < 3; n++ {
				require.NoError(t, w.step(ctx))
			}
			var status string
			var deleted bool
			require.NoError(t, db.QueryRow(`SELECT status FROM wishteam_items LIMIT 1`).Scan(&status))
			require.NoError(t, db.QueryRow(`SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1`, oldID).Scan(&deleted))
			switch scenario {
			case "healthy_empty":
				require.Equal(t, "alive", status)
				require.False(t, deleted)
			case "wrong_mode":
				require.Equal(t, "failed", status)
				require.False(t, deleted)
			default:
				require.Equal(t, "replaced", status)
				require.True(t, deleted)
			}
			require.Equal(t, 1, posts)
		})
	}
}
