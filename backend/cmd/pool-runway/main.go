// A local-only calculation/inspection CLI. No upstream clients are linked.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/poolrunway"
	_ "github.com/lib/pq"
)

func main() {
	demo := flag.Bool("demo", false, "emit explicitly synthetic snapshot; no database access")
	collect := flag.Bool("collect", false, "wait for next 5-minute boundary and collect once")
	policy := flag.String("policy", poolrunway.DefaultPolicy, "known_only or unknown_full_v1")
	flag.Parse()
	if *policy != "known_only" && *policy != poolrunway.DefaultPolicy {
		fmt.Fprintln(os.Stderr, "invalid policy")
		os.Exit(2)
	}
	if *demo {
		emit(demoSnapshot(*policy))
		return
	}
	db, err := sql.Open("postgres", os.Getenv("POOL_RUNWAY_DATABASE_URL"))
	if err != nil {
		fail()
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	w := poolrunway.New(db)
	if *collect {
		next := time.Now().UTC().Truncate(poolrunway.Interval).Add(poolrunway.Interval)
		fmt.Fprintln(os.Stderr, "Waiting for aligned bucket:", next.Format(time.RFC3339))
		time.Sleep(time.Until(next))
		if err = w.Collect(context.Background(), time.Now().UTC()); err != nil {
			fail()
		}
	}
	o, err := w.Overview(context.Background(), *policy)
	if err != nil {
		fail()
	}
	emit(o)
}
func fail() {
	fmt.Fprintln(os.Stderr, "Local monitoring operation failed; check database connectivity, migration and group configuration.")
	os.Exit(1)
}
func emit(v any) {
	b, err := poolrunway.PublicJSON(v)
	if err != nil {
		fail()
	}
	var pretty any
	_ = json.Unmarshal(b, &pretty)
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if e.Encode(pretty) != nil {
		fail()
	}
}
func demoSnapshot(policy string) poolrunway.Snapshot {
	end := time.Now().UTC().Truncate(poolrunway.Interval)
	samples := []poolrunway.Sample{}
	for i := 0; i < 5; i++ {
		at := end.Add(time.Duration(i-4) * poolrunway.Interval)
		records := []poolrunway.Record{}
		for j := 0; j < 571; j++ {
			var percent any
			if j < 315 {
				percent = (1-176.12/315)*100 - float64(4-i)*(46.08/3/4/315)*100
			}
			cache := map[string]any{"codex_usage_updated_at": at.Format(time.RFC3339), "codex_7d_used_percent": percent, "codex_7d_window_minutes": 10080, "codex_7d_reset_at": end.Add(7 * 24 * time.Hour).Format(time.RFC3339)}
			records = append(records, poolrunway.Record{Email: fmt.Sprintf("fixture-%d@example.invalid", j), Workspace: "synthetic-workspace", Platform: "openai", Type: "oauth", Status: "active", Schedulable: true, CredentialPresent: true, ProxyOK: true, Created: end.Add(-time.Duration(j/25+60) * time.Minute), Plan: "team", Cache: cache})
		}
		samples = append(samples, poolrunway.Normalize(records, at))
	}
	r := poolrunway.Calculate(samples[4], samples, policy)
	history := []poolrunway.Result{}
	for i := range samples {
		history = append(history, poolrunway.Calculate(samples[i], samples[:i+1], policy))
	}
	return poolrunway.Snapshot{Schema: "1", Generated: &end, Next: end.Add(poolrunway.Interval), Age: time.Since(end).Seconds(), Available: true, Interval: 300, Group: poolrunway.Group{ID: 0, Name: "模拟数据 · 非生产"}, Policy: policy, Method: poolrunway.Method, Data: &r, History: history}
}
