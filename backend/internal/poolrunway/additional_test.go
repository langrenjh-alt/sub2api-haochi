package poolrunway

import (
	"testing"
	"time"
)

func TestFiveHourIndependentAndResetDrift(t *testing.T) {
	r := record(epoch, 50.)
	r.Cache["codex_5h_used_percent"] = 25.
	r.Cache["codex_5h_window_minutes"] = 300
	r.Cache["codex_5h_reset_at"] = epoch.Add(time.Hour).Format(time.RFC3339)
	s := Normalize([]Record{r}, epoch)
	result := Calculate(s, nil, DefaultPolicy)
	closeTo(t, result.Week.Known, .5)
	closeTo(t, result.FiveHour.Known, .75)
	points := five(10, 15, 20, 25, 30)
	key := identity(r)
	a := points[2].Accounts[key]
	a.Week.Reset = a.Week.Reset.Add(301 * time.Second)
	points[2].Accounts[key] = a
	b := RollingBurn(points, points[4])
	if b.Resets != 2 {
		t.Fatal(b)
	}
	closeTo(t, b.Delta, .1)
	points[2].Method = "different-normalizer"
	if RollingBurn(points, points[4]).Complete {
		t.Fatal("mixed normalization versions")
	}
}

func TestIdentityConflictFlagAndFutureCreation(t *testing.T) {
	r := record(epoch, 0.)
	r.IdentityConflict = true
	if len(Normalize([]Record{r}, epoch).Accounts) != 0 {
		t.Fatal("conflicting identity accepted")
	}
	r.IdentityConflict = false
	r.Created = epoch.Add(2 * time.Minute)
	for _, a := range Normalize([]Record{r}, epoch).Accounts {
		if !a.Created.IsZero() {
			t.Fatal("future batch accepted")
		}
	}
}
