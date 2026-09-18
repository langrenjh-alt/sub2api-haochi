package poolrunway

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

func record(now time.Time, used any) Record {
	return Record{Email: " Example@EXAMPLE.com ", Workspace: "workspace-full-123", Platform: "openai", Type: "oauth", Status: "active", Schedulable: true, CredentialPresent: true, ProxyOK: true, Created: epoch.Add(-time.Hour), Plan: "team",
		Cache: map[string]any{"codex_usage_updated_at": now.Format(time.RFC3339), "codex_7d_used_percent": used, "codex_7d_window_minutes": 10080, "codex_7d_reset_at": epoch.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}
}
func five(values ...float64) []Sample {
	out := []Sample{}
	for i, v := range values {
		now := epoch.Add(time.Duration(i) * Interval)
		out = append(out, Normalize([]Record{record(now, v)}, now))
	}
	return out
}
func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("got %v want %v", got, want)
	}
}
func TestIdentityAndNewestObservation(t *testing.T) {
	a := record(epoch, 20.)
	b := record(epoch.Add(time.Minute), 50.)
	b.Schedulable = false
	c := record(epoch, 30.)
	c.Workspace = "workspace-full-456"
	s := Normalize([]Record{a, b, c}, epoch.Add(time.Minute))
	r := Calculate(s, nil, DefaultPolicy)
	if r.Candidates != 2 || s.Duplicates != 1 {
		t.Fatal(r)
	}
	closeTo(t, r.Week.Known, 1.2)
	for _, kind := range []string{"email_missing", "workspace_missing", "email_conflict", "workspace_conflict"} {
		x := a
		switch kind {
		case "email_missing":
			x.Email = ""
		case "workspace_missing":
			x.Workspace = ""
		case "email_conflict":
			x.ExtraEmail = "other@example.com"
		case "workspace_conflict":
			x.ExtraWorkspace = "other"
		}
		s = Normalize([]Record{x, x}, epoch)
		if len(s.Accounts) != 0 || s.Excluded["unreliable_identity"] != 2 {
			t.Fatal(kind, s)
		}
	}
}
func TestExclusions(t *testing.T) {
	tests := map[string]func(*Record){
		"error": func(r *Record) { r.Status = "error" }, "disabled": func(r *Record) { r.Status = "disabled" },
		"not_schedulable": func(r *Record) { r.Schedulable = false }, "missing_credential": func(r *Record) { r.CredentialPresent = false },
		"expired": func(r *Record) { r.Expires = epoch; r.AutoPause = true }, "rate_limit": func(r *Record) { r.RateLimit = epoch.Add(time.Hour) },
		"overload": func(r *Record) { r.Overload = epoch.Add(time.Hour) }, "temporary": func(r *Record) { r.Temporary = epoch.Add(time.Hour) },
		"proxy_unavailable": func(r *Record) { r.ProxyOK = false }, "key_or_other_type": func(r *Record) { r.Type = "apikey" },
		"shadow": func(r *Record) { r.Shadow = true }, "other_platform": func(r *Record) { r.Platform = "anthropic" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := record(epoch, 0.)
			mutate(&r)
			s := Normalize([]Record{r}, epoch)
			result := Calculate(s, nil, DefaultPolicy)
			if result.Candidates != 0 || s.Excluded[name] != 1 {
				t.Fatal(result)
			}
		})
	}
	if Calculate(Normalize(nil, epoch), nil, DefaultPolicy).Forecast.Status != "empty_pool" {
		t.Fatal("empty")
	}
}
func TestQuotaValidation(t *testing.T) {
	for _, v := range []any{nil, true, false, "", "0", math.NaN(), math.Inf(1), -1., 101.} {
		r := record(epoch, v)
		if ReadQuota(r.Cache, "7d", time.Time{}, epoch).Known {
			t.Fatalf("accepted %v", v)
		}
	}
	for _, v := range []float64{0, 100} {
		r := record(epoch, v)
		q := ReadQuota(r.Cache, "7d", time.Time{}, epoch)
		if !q.Known || q.Used != v {
			t.Fatal(q)
		}
	}
	for _, tc := range []struct {
		field  string
		v      any
		reason string
	}{
		{"codex_usage_updated_at", "bad", "invalid_updated_at"},
		{"codex_usage_updated_at", epoch.Add(61 * time.Second).Format(time.RFC3339), "invalid_updated_at"},
		{"codex_7d_reset_at", epoch.Format(time.RFC3339), "reset_unknown_or_elapsed"},
		{"codex_7d_window_minutes", 300, "wrong_window"},
	} {
		r := record(epoch, 0.)
		r.Cache[tc.field] = tc.v
		q := ReadQuota(r.Cache, "7d", epoch, epoch)
		if q.Known || q.Reason != tc.reason {
			t.Fatal(q, tc)
		}
	}
	r := record(epoch, 0.)
	now := epoch.Add(16 * time.Minute)
	if q := ReadQuota(r.Cache, "7d", epoch, now); !q.Known || q.Reason != "idle_cached" {
		t.Fatal(q)
	}
	if q := ReadQuota(r.Cache, "7d", epoch.Add(time.Minute), now); q.Known || q.Reason != "stale" {
		t.Fatal(q)
	}
	// All copies' last use is considered, not only the cache-owning copy.
	b := r
	b.LastUsed = epoch.Add(time.Minute)
	s := Normalize([]Record{r, b}, now)
	for _, a := range s.Accounts {
		if a.Week.Known {
			t.Fatal("stale replica accepted")
		}
	}
}
func TestConflictingDuplicateObservations(t *testing.T) {
	a := record(epoch, 20.)
	b := record(epoch, 40.)
	for _, rs := range [][]Record{{a, b, a}, {b, a, b}, {a, b}} {
		for _, account := range Normalize(rs, epoch).Accounts {
			if account.Week.Known || account.Week.Reason != "conflicting_observation" {
				t.Fatal(account)
			}
		}
	}
}
func TestTwentyMinuteArithmetic(t *testing.T) {
	for _, tc := range []struct {
		values      []float64
		delta, rate float64
		resets      int
	}{
		{[]float64{10, 15, 20, 25, 30}, .2, .6, 0},
		{[]float64{90, 95, 2, 7, 12}, .15, .45, 1},
	} {
		s := five(tc.values...)
		r := Calculate(s[4], s, DefaultPolicy)
		closeTo(t, r.Burn.Delta, tc.delta)
		closeTo(t, r.Burn.Rate, tc.rate)
		if !r.Burn.Complete || r.Burn.Paired != 1 || r.Burn.Resets != tc.resets || r.Forecast.Status != "estimated" {
			t.Fatal(r)
		}
	}
}
func TestChurnAndUnion(t *testing.T) {
	s := five(10, 15, 20, 25, 30)
	key := identity(record(epoch, 0.))
	// Exit after second segment: keep both earlier segments, no extrapolation.
	delete(s[3].Accounts, key)
	delete(s[4].Accounts, key)
	b := RollingBurn(s, s[4])
	closeTo(t, b.Delta, .1)
	if b.Paired != 1 || b.CurrentPaired != 0 {
		t.Fatal(b)
	}
	s = five(10, 15, 20, 25, 30)
	delete(s[0].Accounts, key)
	delete(s[1].Accounts, key)
	b = RollingBurn(s, s[4])
	closeTo(t, b.Delta, .1)
	if b.Paired != 1 {
		t.Fatal(b)
	}
}
func TestUnchangedAndBackwardsObservation(t *testing.T) {
	s := five(10, 15, 20, 25, 30)
	key := identity(record(epoch, 0.))
	for i := range s {
		a := s[i].Accounts[key]
		a.Week.Updated = epoch
		s[i].Accounts[key] = a
	}
	b := RollingBurn(s, s[4])
	if b.Delta != 0 || b.Anomalies != 4 || b.CurrentPaired != 0 {
		t.Fatal(b)
	}
	for i := range s {
		a := s[i].Accounts[key]
		a.Week.Updated = epoch.Add(-time.Duration(i) * time.Minute)
		s[i].Accounts[key] = a
	}
	if b = RollingBurn(s, s[4]); b.Anomalies != 4 || b.Delta != 0 {
		t.Fatal(b)
	}
	s = five(10, 10, 10, 10, 10)
	if r := Calculate(s[4], s, DefaultPolicy); r.Forecast.Status != "no_observed_burn" || r.Forecast.Hours != nil {
		t.Fatal(r)
	}
}
func TestSamplingBuckets(t *testing.T) {
	s := five(10, 15, 20, 25, 30)
	current := s[4]
	if RollingBurn(s[1:], current).Complete {
		t.Fatal("15 minutes accepted")
	}
	for i := range s {
		s[i].At = s[i].At.Add(29 * time.Second)
	}
	if !RollingBurn(s, current).Complete {
		t.Fatal("valid jitter rejected")
	}
	s[2].At = s[2].At.Add(2 * time.Second)
	if RollingBurn(s, current).Complete {
		t.Fatal("manual unaligned sample accepted")
	}
	if RollingBurn([]Sample{current, current, current, current, current}, current).Complete {
		t.Fatal("reused snapshot")
	}
}
func TestUnknownAssumptionNeverBurns(t *testing.T) {
	s := five(10, 15, 20, 25, 30)
	key := identity(record(epoch, 0.))
	for i := 0; i < 4; i++ {
		a := s[i].Accounts[key]
		a.Week = Quota{Reason: "missing"}
		s[i].Accounts[key] = a
	}
	r := Calculate(s[4], s, DefaultPolicy)
	if r.Burn.Delta != 0 || r.Forecast.Status != "insufficient_samples" {
		t.Fatal(r)
	}
	s = five(10, 15, 20, 25, 30)
	for i := range s {
		a := record(s[i].At, nil)
		a.Workspace = "unknown-workspace"
		n := Normalize([]Record{a}, s[i].At)
		for k, v := range n.Accounts {
			s[i].Accounts[k] = v
		}
	}
	a := Calculate(s[4], s, DefaultPolicy)
	b := Calculate(s[4], s, "known_only")
	closeTo(t, a.Week.Assumed, 1)
	closeTo(t, b.Week.Assumed, 0)
	closeTo(t, a.Burn.Rate, b.Burn.Rate)
	closeTo(t, a.FiveHour.Assumed, 0)
}
func TestFixed571Example(t *testing.T) {
	i := Inventory{Known: 176.12, Assumed: 256, Remaining: 432.12, KnownAccounts: 315, UnknownAccounts: 256, Coverage: 315. / 571 * 100}
	b := Burn{Complete: true, Rate: 46.08, CurrentPaired: 315, Coverage: 100}
	f := Predict(i, b, epoch, DefaultPolicy)
	closeTo(t, *f.Hours, 9.377604166666666)
	closeTo(t, *f.KnownHours, 3.822048611111111)
	if f.Confidence != "low" || f.Status != "estimated" {
		t.Fatal(f)
	}
	t.Logf("571 candidates K=%.2f A=%.0f S=%.2f R=%.2f coverage=%.2f%% planned=%.2fh known=%.2fh confidence=%s", i.Known, i.Assumed, i.Remaining, b.Rate, i.Coverage, *f.Hours, *f.KnownHours, f.Confidence)
}
func TestForecastStatesAndThreshold(t *testing.T) {
	s := five(100, 100, 100, 100, 100)
	if r := Calculate(s[4], s, DefaultPolicy); r.Forecast.Status != "exhausted" {
		t.Fatal(r)
	}
	i := Inventory{UnknownAccounts: 100, Assumed: 100, Remaining: 100}
	if f := Predict(i, Burn{}, epoch, "known_only"); f.Status != "unknown_quota" {
		t.Fatal(f)
	}
	if f := Predict(i, Burn{Complete: true}, epoch, DefaultPolicy); f.Status == "exhausted" || f.Hours != nil {
		t.Fatal(f)
	}
	i = Inventory{Known: 10, Remaining: 10, KnownAccounts: 10, Coverage: 100}
	b := Burn{Complete: true, Rate: .001, CurrentPaired: 2, Coverage: 20}
	if f := Predict(i, b, epoch, "known_only"); f.Status != "insufficient_samples" {
		t.Fatal(f)
	}
	b.CurrentPaired = 3
	b.Coverage = 30
	if f := Predict(i, b, epoch, "known_only"); !f.OverSevenDays || f.ETA != nil {
		t.Fatal(f)
	}
}
func TestPlanBatchAndPublicOutput(t *testing.T) {
	a := record(epoch, 0.)
	b := record(epoch, nil)
	b.Workspace = "second"
	b.Plan = "pro"
	b.Created = epoch
	s := Normalize([]Record{a, b}, epoch)
	r := Calculate(s, nil, DefaultPolicy)
	if !r.MixedPlans || len(r.Batches) != 2 {
		t.Fatal(r)
	}
	raw, err := PublicJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"example.com", "workspace-full", "access_token", "credentials", identity(a)} {
		if strings.Contains(string(raw), value) {
			t.Fatal("sensitive output")
		}
	}
	for _, name := range []string{"access_token", "refresh_token", "id_token", "Cookie", "password", "admin_secret", "email", "credentials", "workspace_id"} {
		if _, err = PublicJSON(map[string]any{"nested": []any{map[string]any{name: "value"}}}); err == nil {
			t.Fatal(name)
		}
	}
	var decoded Result
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
}
