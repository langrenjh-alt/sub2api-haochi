// Package poolrunway observes local cached quotas only. It never calls a provider.
package poolrunway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

const Method = "adjacent20m-equivalent-v1"
const DefaultPolicy = "unknown_full_v1"
const Interval = 5 * time.Minute

type Record struct {
	IdentityConflict                                           bool
	ID                                                         int64
	Email, ExtraEmail, Workspace, ExtraWorkspace               string
	Platform, Type, Status, Plan                               string
	Shadow, Schedulable, CredentialPresent, ProxyOK            bool
	AutoPause                                                  bool
	Created, LastUsed, Expires, RateLimit, Overload, Temporary time.Time
	Cache                                                      map[string]any
}
type Quota struct {
	Known   bool      `json:"known"`
	Used    float64   `json:"used"`
	Updated time.Time `json:"updated"`
	Reset   time.Time `json:"reset"`
	Reason  string    `json:"reason"`
}
type Account struct {
	Key       string    `json:"key"`
	Candidate bool      `json:"candidate"`
	Created   time.Time `json:"created"`
	Plan      string    `json:"plan"`
	Week      Quota     `json:"week"`
	FiveHour  Quota     `json:"five_hour"`
}
type Sample struct {
	Method     string             `json:"method_version"`
	At         time.Time          `json:"at"`
	Accounts   map[string]Account `json:"accounts"`
	Excluded   map[string]int     `json:"excluded"`
	Duplicates int                `json:"duplicates"`
}
type Inventory struct {
	Known           float64        `json:"known_remaining_equivalents"`
	Assumed         float64        `json:"assumed_remaining_equivalents"`
	Remaining       float64        `json:"remaining_equivalents"`
	KnownAccounts   int            `json:"known_accounts"`
	UnknownAccounts int            `json:"unknown_accounts"`
	Coverage        float64        `json:"coverage_percent"`
	IdleCached      int            `json:"idle_cached_accounts"`
	Reasons         map[string]int `json:"unknown_reasons"`
}
type Segment struct {
	Start  time.Time `json:"start_at"`
	End    time.Time `json:"end_at"`
	Delta  float64   `json:"delta_equivalents"`
	Paired int       `json:"paired_accounts"`
	Resets int       `json:"reset_pairs_excluded"`
}
type Burn struct {
	Start         time.Time `json:"window_start_at"`
	End           time.Time `json:"window_end_at"`
	SegmentCount  int       `json:"segment_count"`
	Complete      bool      `json:"complete"`
	Delta         float64   `json:"delta_equivalents"`
	Rate          float64   `json:"equivalents_per_hour"`
	Paired        int       `json:"paired_accounts"`
	CurrentPaired int       `json:"current_paired_accounts"`
	Consuming     int       `json:"consuming_accounts"`
	Updated       int       `json:"updated_accounts"`
	Resets        int       `json:"reset_pairs_excluded"`
	Anomalies     int       `json:"anomalous_pairs"`
	Coverage      float64   `json:"coverage_percent"`
	Segments      []Segment `json:"segments"`
}
type Forecast struct {
	Status        string     `json:"status"`
	Confidence    string     `json:"confidence"`
	Hours         *float64   `json:"remaining_hours"`
	KnownHours    *float64   `json:"known_hours"`
	ETA           *time.Time `json:"available_until"`
	OverSevenDays bool       `json:"over_seven_days"`
}
type Batch struct {
	At         time.Time `json:"at"`
	Candidates int       `json:"candidates"`
	Known      int       `json:"known"`
	Unknown    int       `json:"unknown"`
	Remaining  float64   `json:"remaining_equivalents"`
}
type Result struct {
	Generated  time.Time      `json:"generated_at"`
	Policy     string         `json:"forecast_policy"`
	Method     string         `json:"method_version"`
	Candidates int            `json:"candidates"`
	Week       Inventory      `json:"week"`
	FiveHour   Inventory      `json:"five_hour"`
	Burn       Burn           `json:"burn"`
	Forecast   Forecast       `json:"forecast"`
	Excluded   map[string]int `json:"excluded_records"`
	Duplicates int            `json:"duplicate_records_collapsed"`
	MixedPlans bool           `json:"mixed_plans"`
	Batches    []Batch        `json:"batches"`
}

func number(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case json.Number:
		var err error
		f, err = n.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}
func timestamp(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t.UTC()
}
func ReadQuota(cache map[string]any, window string, lastUsed, now time.Time) Quota {
	q := Quota{Reason: "missing"}
	v, exists := cache["codex_"+window+"_used_percent"]
	if !exists || v == nil {
		return q
	}
	used, ok := number(v)
	if !ok || used < 0 || used > 100 {
		q.Reason = "invalid_percent"
		return q
	}
	minutes, ok := number(cache["codex_"+window+"_window_minutes"])
	expected := 10080.
	if window == "5h" {
		expected = 300
	}
	if !ok || minutes != expected {
		q.Reason = "wrong_window"
		return q
	}
	q.Updated = timestamp(cache["codex_usage_updated_at"])
	if q.Updated.IsZero() || q.Updated.After(now.Add(time.Minute)) {
		q.Reason = "invalid_updated_at"
		return q
	}
	q.Reset = timestamp(cache["codex_"+window+"_reset_at"])
	if q.Reset.IsZero() || !q.Reset.After(now) {
		q.Reason = "reset_unknown_or_elapsed"
		return q
	}
	q.Reason = "fresh"
	if now.Sub(q.Updated) > 15*time.Minute {
		if lastUsed.After(q.Updated) {
			q.Reason = "stale"
			return q
		}
		q.Reason = "idle_cached"
	}
	q.Known = true
	q.Used = used
	return q
}

// Workspace IDs are opaque, trimmed but NOT truncated or case-folded.
func identity(r Record) string {
	if r.IdentityConflict {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(r.Email))
	extra := strings.ToLower(strings.TrimSpace(r.ExtraEmail))
	if email == "" {
		email = extra
	}
	ws := strings.TrimSpace(r.Workspace)
	ew := strings.TrimSpace(r.ExtraWorkspace)
	if ws == "" {
		ws = ew
	}
	if email == "" || ws == "" || (extra != "" && extra != email) || (ew != "" && ew != ws) {
		return ""
	}
	sum := sha256.Sum256([]byte(email + "\x00" + ws))
	return hex.EncodeToString(sum[:])
}
func exclusion(r Record, now time.Time) string {
	if r.Platform != "openai" {
		return "other_platform"
	}
	if r.Type != "oauth" {
		return "key_or_other_type"
	}
	if r.Shadow {
		return "shadow"
	}
	if r.Status != "active" {
		if r.Status == "error" {
			return "error"
		}
		return "disabled"
	}
	if !r.Schedulable {
		return "not_schedulable"
	}
	if !r.CredentialPresent {
		return "missing_credential"
	}
	if r.AutoPause && !r.Expires.IsZero() && !r.Expires.After(now) {
		return "expired"
	}
	if r.RateLimit.After(now) {
		return "rate_limit"
	}
	if r.Overload.After(now) {
		return "overload"
	}
	if r.Temporary.After(now) {
		return "temporary"
	}
	if !r.ProxyOK {
		return "proxy_unavailable"
	}
	return ""
}
func Normalize(records []Record, now time.Time) Sample {
	s := Sample{At: now.UTC(), Method: Method, Accounts: map[string]Account{}, Excluded: map[string]int{}}
	groups := map[string][]Record{}
	for _, r := range records {
		reason := exclusion(r, now)
		if reason != "" {
			s.Excluded[reason]++
		}
		if r.Platform != "openai" || r.Type != "oauth" || r.Shadow {
			continue
		}
		key := identity(r)
		if key == "" {
			s.Excluded["unreliable_identity"]++
			continue
		}
		groups[key] = append(groups[key], r)
	}
	for key, rs := range groups {
		a := Account{Key: key}
		var last time.Time
		for _, r := range rs {
			a.Candidate = a.Candidate || exclusion(r, now) == ""
			if r.Created.After(time.Time{}) && !r.Created.After(now.Add(time.Minute)) && (a.Created.IsZero() || r.Created.Before(a.Created)) {
				a.Created = r.Created.UTC()
			}
			if r.LastUsed.After(last) {
				last = r.LastUsed
			}
		}
		s.Duplicates += len(rs) - 1
		a.Week, a.Plan = latestQuota(rs, "7d", last, now)
		a.FiveHour, _ = latestQuota(rs, "5h", last, now)
		s.Accounts[key] = a
	}
	return s
}
func latestQuota(rs []Record, window string, last, now time.Time) (Quota, string) {
	best := Quota{Reason: "missing"}
	p := "unknown"
	for i, r := range rs {
		q := ReadQuota(r.Cache, window, last, now)
		if i == 0 || (!best.Known && q.Known) || (best.Known == q.Known && q.Updated.After(best.Updated)) {
			best = q
			p = plan(r.Plan)
		}
	}
	if best.Known {
		for _, r := range rs {
			q := ReadQuota(r.Cache, window, last, now)
			if q.Known && q.Updated.Equal(best.Updated) && (q.Used != best.Used || !q.Reset.Equal(best.Reset)) {
				return Quota{Reason: "conflicting_observation", Updated: best.Updated}, p
			}
		}
	}
	return best, p
}
func plan(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "free", "plus", "pro", "team", "business", "enterprise":
		return strings.ToLower(strings.TrimSpace(s))
	}
	return "unknown"
}
func inventory(accounts map[string]Account, week bool, policy string) Inventory {
	i := Inventory{Reasons: map[string]int{}}
	for _, a := range accounts {
		if !a.Candidate {
			continue
		}
		q := a.Week
		if !week {
			q = a.FiveHour
		}
		if q.Known {
			i.KnownAccounts++
			i.Known += (100 - q.Used) / 100
			if q.Reason == "idle_cached" {
				i.IdleCached++
			}
		} else {
			i.UnknownAccounts++
			i.Reasons[q.Reason]++
		}
	}
	if week && policy == DefaultPolicy {
		i.Assumed = float64(i.UnknownAccounts)
	}
	i.Remaining = i.Known + i.Assumed
	if n := i.KnownAccounts + i.UnknownAccounts; n > 0 {
		i.Coverage = 100 * float64(i.KnownAccounts) / float64(n)
	}
	return i
}

// RollingBurn never uses changes in aggregate inventory as consumption.
func RollingBurn(samples []Sample, current Sample) Burn {
	end := current.At.Truncate(Interval)
	b := Burn{Start: end.Add(-20 * time.Minute), End: end, SegmentCount: 4, Segments: []Segment{}}
	points := make([]*Sample, 5)
	used := map[int]bool{}
	for j := 0; j < 5; j++ {
		target := b.Start.Add(time.Duration(j) * Interval)
		best := 31 * time.Second
		index := -1
		for k := range samples {
			if samples[k].Method != Method {
				continue
			}
			d := samples[k].At.Sub(target)
			if d < 0 {
				d = -d
			}
			if !used[k] && d <= 30*time.Second && d < best {
				best = d
				index = k
			}
		}
		if index < 0 {
			return b
		}
		used[index] = true
		points[j] = &samples[index]
	}
	paired := map[string]bool{}
	consuming := map[string]bool{}
	updated := map[string]bool{}
	for j := 0; j < 4; j++ {
		seg := Segment{Start: b.Start.Add(time.Duration(j) * Interval), End: b.Start.Add(time.Duration(j+1) * Interval)}
		for key, prev := range points[j].Accounts {
			next, ok := points[j+1].Accounts[key]
			if !ok || (!prev.Candidate && !next.Candidate) || !prev.Week.Known || !next.Week.Known {
				continue
			}
			p, n := prev.Week, next.Week
			if n.Updated.After(p.Updated) {
				updated[key] = true
			}
			if math.Abs(n.Reset.Sub(p.Reset).Seconds()) > 300 || n.Used < p.Used {
				seg.Resets++
				continue
			}
			if n.Updated.Before(p.Updated) || (n.Updated.Equal(p.Updated) && n.Used != p.Used) {
				b.Anomalies++
				continue
			}
			if !n.Updated.After(p.Updated) {
				continue
			}
			updated[key] = true
			paired[key] = true
			seg.Paired++
			d := (n.Used - p.Used) / 100
			seg.Delta += d
			if d > 0 {
				consuming[key] = true
			}
		}
		b.Delta += seg.Delta
		b.Resets += seg.Resets
		b.Segments = append(b.Segments, seg)
	}
	b.Complete = true
	b.Rate = b.Delta * 3
	b.Paired = len(paired)
	b.Consuming = len(consuming)
	b.Updated = len(updated)
	known := 0
	for key, a := range current.Accounts {
		if a.Candidate && a.Week.Known {
			known++
			if paired[key] {
				b.CurrentPaired++
			}
		}
	}
	if known > 0 {
		b.Coverage = 100 * float64(b.CurrentPaired) / float64(known)
	}
	return b
}
func Predict(i Inventory, b Burn, now time.Time, policy string) Forecast {
	f := Forecast{Status: "warming", Confidence: "planning_reference"}
	if i.Coverage < 80 || b.Coverage < 60 {
		f.Confidence = "low"
	}
	switch {
	case i.KnownAccounts+i.UnknownAccounts == 0:
		f.Status = "empty_pool"
	case i.UnknownAccounts == 0 && i.Known == 0:
		f.Status = "exhausted"
	case policy == "known_only" && i.KnownAccounts == 0:
		f.Status = "unknown_quota"
	case !b.Complete:
		return f
	case b.CurrentPaired < int(math.Max(1, math.Ceil(float64(i.KnownAccounts)*.3))):
		f.Status = "insufficient_samples"
	case b.Rate <= 0:
		f.Status = "no_observed_burn"
	default:
		f.Status = "estimated"
		h := i.Remaining / b.Rate
		kh := i.Known / b.Rate
		f.Hours = &h
		f.KnownHours = &kh
		if h > 168 {
			f.OverSevenDays = true
		} else {
			eta := now.Add(time.Duration(h * float64(time.Hour)))
			f.ETA = &eta
		}
	}
	return f
}
func Calculate(current Sample, samples []Sample, policy string) Result {
	if policy != "known_only" {
		policy = DefaultPolicy
	}
	r := Result{Generated: current.At, Policy: policy, Method: Method, Excluded: current.Excluded, Duplicates: current.Duplicates, Batches: []Batch{}}
	r.Week = inventory(current.Accounts, true, policy)
	r.FiveHour = inventory(current.Accounts, false, "known_only")
	r.Candidates = r.Week.KnownAccounts + r.Week.UnknownAccounts
	r.Burn = RollingBurn(samples, current)
	r.Forecast = Predict(r.Week, r.Burn, current.At, policy)
	plans := map[string]bool{}
	batches := map[time.Time]*Batch{}
	for _, a := range current.Accounts {
		if !a.Candidate {
			continue
		}
		plans[a.Plan] = true
		at := a.Created.Truncate(time.Minute)
		if batches[at] == nil {
			batches[at] = &Batch{At: at}
		}
		b := batches[at]
		b.Candidates++
		if a.Week.Known {
			b.Known++
			b.Remaining += (100 - a.Week.Used) / 100
		} else {
			b.Unknown++
			if policy == DefaultPolicy {
				b.Remaining++
			}
		}
	}
	r.MixedPlans = len(plans) > 1
	for _, b := range batches {
		r.Batches = append(r.Batches, *b)
	}
	sort.Slice(r.Batches, func(i, j int) bool { return r.Batches[i].At.Before(r.Batches[j].At) })
	return r
}

// Public output only contains aggregate typed DTOs. Defense-in-depth recursive key check.
func PublicJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj any
	if err = json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	var check func(any) bool
	check = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				key := strings.ToLower(k)
				for _, bad := range []string{"token", "cookie", "password", "credential", "email", "secret", "identity", "workspace", "authorization", "api_key"} {
					if strings.Contains(key, bad) {
						return false
					}
				}
				if !check(val) {
					return false
				}
			}
		case []any:
			for _, val := range x {
				if !check(val) {
					return false
				}
			}
		}
		return true
	}
	if !check(obj) {
		return nil, &json.UnsupportedValueError{Str: "sensitive output field"}
	}
	return raw, nil
}
