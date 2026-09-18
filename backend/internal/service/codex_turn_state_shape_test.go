package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// synthTurnState builds a Fernet-shaped token whose ciphertext is exactly
// `blocks` AES blocks, so the shape classifier can be exercised without a live
// upstream. The real tokens are 292/312/332/356 characters for 10/11/12/13
// blocks, which is the whole basis of the shape table.
func synthTurnState(blocks int, issued time.Time) string {
	raw := make([]byte, 0, codexTurnStateFernetOverhead+blocks*16)
	raw = append(raw, 0x80)
	ts := make([]byte, 8)
	for i := 0; i < 8; i++ {
		ts[7-i] = byte(uint64(issued.Unix()) >> (8 * i))
	}
	raw = append(raw, ts...)
	raw = append(raw, make([]byte, 16)...) // IV
	raw = append(raw, make([]byte, blocks*16)...)
	raw = append(raw, make([]byte, 32)...) // HMAC
	return base64.URLEncoding.EncodeToString(raw)
}

// The table is the contract: an account class only ever uses these four shapes,
// and the degraded variant is exactly one block above its own baseline.
func TestClassifyCodexTurnStateReadsTheShapeTable(t *testing.T) {
	now := time.Unix(1789716000, 0).UTC()
	cases := []struct {
		blocks int
		chars  int
		shape  string
		normal bool
	}{
		{10, 292, CodexTurnStateShapeIndividual, true},
		{11, 312, CodexTurnStateShapeIndividual, false},
		{12, 332, CodexTurnStateShapeTeam, true},
		{13, 356, CodexTurnStateShapeTeam, false},
	}
	for _, tc := range cases {
		token := synthTurnState(tc.blocks, now)
		if len(token) != tc.chars {
			t.Fatalf("synthesized %d-block token is %d chars, the table says %d", tc.blocks, len(token), tc.chars)
		}
		info := ClassifyCodexTurnState(token)
		if info.Blocks != tc.blocks || info.Chars != tc.chars {
			t.Errorf("%d-char token: blocks=%d chars=%d, want %d/%d", tc.chars, info.Blocks, info.Chars, tc.blocks, tc.chars)
		}
		if info.Shape != tc.shape || info.Normal != tc.normal {
			t.Errorf("%d-block token: shape=%q normal=%v, want %q/%v", tc.blocks, info.Shape, info.Normal, tc.shape, tc.normal)
		}
	}
}

// Anything unreadable is unknown, never degraded: "we cannot tell" must not be
// reported as a downgrade, or a format change would empty the pool silently.
func TestClassifyCodexTurnStateTreatsGarbageAsUnknown(t *testing.T) {
	for _, token := range []string{"", "   ", "not-a-token", "eyJhbGciOiJSUzI1NiJ9.abc.def"} {
		info := ClassifyCodexTurnState(token)
		if info.Shape != CodexTurnStateShapeUnknown {
			t.Errorf("%q classified as %q, want unknown", token, info.Shape)
		}
		if info.Normal {
			t.Errorf("%q reported as normal", token)
		}
	}
}

func newShapeTestService(t *testing.T, cfg CodexTurnStateConfig) (*CodexTurnStateService, *fakeTurnStateRepo, time.Time) {
	t.Helper()
	repo := newFakeTurnStateRepo()
	cfg.Enabled = true
	cfg.InjectEnabled = true
	cfg.AccountIDs = []int64{7}
	repo.cfg = cfg
	svc := NewCodexTurnStateService(repo, nil, nil, nil)
	now := time.Unix(1789716000, 0).UTC()
	svc.now = func() time.Time { return now }
	// 预热配置缓存，让这一次读取就用上面注入的时钟。
	svc.reloadConfig(context.Background())
	return svc, repo, now
}

func headerWithState(value string) http.Header {
	h := http.Header{}
	h.Set(CodexTurnStateDefaultHeader, value)
	return h
}

// A token from a downgraded turn is recorded so the history shows what came
// back, but it must never become injectable.
func TestDegradedShapeIsRecordedButNotInjectable(t *testing.T) {
	svc, repo, now := newShapeTestService(t, CodexTurnStateConfig{})
	bad := synthTurnState(13, now)

	svc.persist(context.Background(), codexTurnStateEvent{
		accountID: 7, state: bad, statusCode: 200, model: "gpt-6-astra", source: CodexTurnStateSourceHarvest,
	})

	if len(repo.records) != 1 {
		t.Fatalf("want the degraded token recorded once, got %d records", len(repo.records))
	}
	if got := repo.records[0].Status; got != CodexTurnStateStatusDegraded {
		t.Errorf("status = %q, want %q", got, CodexTurnStateStatusDegraded)
	}
	if _, _, ok := svc.InjectionHeader(7, ""); ok {
		t.Error("a degraded token must not be injectable")
	}
}

// The arrival of a degraded token must not evict a good one: the good state is
// still accepted by upstream, so overwriting it would trade a working route for
// a downgrade.
func TestDegradedShapeDoesNotEvictAGoodState(t *testing.T) {
	svc, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	good := synthTurnState(12, now)
	bad := synthTurnState(13, now)

	svc.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel,
		accountID: 7, state: good, statusCode: 200, source: CodexTurnStateSourceHarvest,
	})
	svc.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel,
		accountID: 7, state: bad, statusCode: 200, source: CodexTurnStateSourceHarvest,
	})

	_, value, ok := svc.InjectionHeader(7, "")
	if !ok || value != good {
		t.Fatalf("injection = (ok=%v, %d chars), want the 332 state to survive", ok, len(value))
	}
}

// AllowDegradedShapes is the documented escape hatch: with it on, a degraded
// token is injectable again, which pins the downgrade on purpose.
func TestLegacyAllowDegradedShapesCannotOverrideStrictTargets(t *testing.T) {
	svc, _, now := newShapeTestService(t, CodexTurnStateConfig{AllowDegradedShapes: true})
	bad := synthTurnState(13, now)

	svc.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel,
		accountID: 7, state: bad, statusCode: 200, source: CodexTurnStateSourceHarvest,
	})

	_, value, ok := svc.InjectionHeader(7, "")
	if ok {
		t.Fatalf("strict target policy admitted a degraded state: %d chars", len(value))
	}
}

func TestForceInjectFollowsConfig(t *testing.T) {
	svc, _, _ := newShapeTestService(t, CodexTurnStateConfig{})
	if svc.ForceInject() {
		t.Error("fill_empty must be the default: the client's own state is preserved")
	}
	svc, _, _ = newShapeTestService(t, CodexTurnStateConfig{InjectMode: CodexTurnStateInjectModeForce})
	if !svc.ForceInject() {
		t.Error("force mode must be reported")
	}
}

// fakeTurnStateGateway records what the hook asked for, so the two injection
// modes and the model allow-list can be told apart at the request level.
type fakeTurnStateGateway struct {
	value     string
	force     bool
	scoped    bool
	lastModel string
}

func (f *fakeTurnStateGateway) InjectionHeader(_ int64, requestedModel string) (string, string, bool) {
	f.lastModel = requestedModel
	if f.value == "" || requestedModel == "skip" {
		return "", "", false
	}
	return CodexTurnStateDefaultHeader, f.value, true
}

func (f *fakeTurnStateGateway) ForceInject() bool { return f.force }

func (f *fakeTurnStateGateway) ModelScoped() bool { return f.scoped }

func (f *fakeTurnStateGateway) ObserveResponse(int64, http.Header, int, string) {}

func TestInjectionMatchesModel(t *testing.T) {
	cases := []struct {
		name   string
		models []string
		asked  string
		want   bool
	}{
		{"no list scopes to the collection model", nil, "gpt-6-astra", true},
		{"no list refuses the other models", nil, "gpt-5.5", false},
		{"* opts into every model", []string{"*"}, "gpt-5.5", true},
		{"exact hit", []string{"gpt-6-astra"}, "gpt-6-astra", true},
		{"case insensitive", []string{"GPT-6-Astra"}, "gpt-6-astra", true},
		{"exact miss", []string{"gpt-6-astra"}, "gpt-5.5", false},
		{"prefix wildcard hits", []string{"gpt-5.*"}, "gpt-5.6-sol", true},
		{"prefix wildcard still needs the prefix", []string{"gpt-5.*"}, "gpt-6-astra", false},
		{"unknown model is injected anyway", []string{"gpt-6-astra"}, "", true},
		{"any of several entries may hit", []string{"gpt-6-astra", "gpt-5.5"}, "gpt-5.5", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{InjectModels: tc.models})
			if got := cfg.InjectionMatchesModel(tc.asked); got != tc.want {
				t.Errorf("InjectionMatchesModel(%q) with %v = %v, want %v", tc.asked, cfg.InjectModels, got, tc.want)
			}
		})
	}
}

func TestValidateCodexTurnStateConfigRejectsBadModelEntries(t *testing.T) {
	for _, bad := range [][]string{{"gpt 6"}, {"gpt-6-astra;rm"}, {strings.Repeat("a", 120)}} {
		cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{InjectModels: bad})
		cfg.Model = CodexTurnStateDefaultModel
		if err := ValidateCodexTurnStateConfig(cfg); err == nil {
			t.Errorf("entries %v must be rejected", bad)
		}
	}
}

// The hook reads the requested model out of the request body — without consuming
// it — only when an allow-list makes that decision meaningful.
func TestInjectCodexTurnStateReadsTheRequestedModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	newRequest := func() *http.Request {
		request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return request
	}

	gateway := &fakeTurnStateGateway{value: "pooled-state", force: true, scoped: true}
	svc := &OpenAIGatewayService{codexTurnState: gateway}
	request := newRequest()
	svc.injectCodexTurnState(request, &Account{ID: 7})
	if gateway.lastModel != "gpt-5.5" {
		t.Errorf("requested model read as %q, want gpt-5.5", gateway.lastModel)
	}
	if request.Header.Get(CodexTurnStateDefaultHeader) != "pooled-state" {
		t.Error("a scoped pool must still inject when it accepts the model")
	}
	// The body must survive the model probe untouched.
	replayed, err := request.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("body was modified by the model probe: %s", got)
	}

	// An unreadable body cannot pass a model-scoped injection policy.
	blind := newRequest()
	blind.GetBody = nil
	gateway.lastModel = "unset"
	svc.injectCodexTurnState(blind, &Account{ID: 7})
	if gateway.lastModel != "unset" {
		t.Errorf("pool consulted for an unreadable model: %q", gateway.lastModel)
	}
	if blind.Header.Get(CodexTurnStateDefaultHeader) != "" {
		t.Error("an unknown model must not receive a model-bound token")
	}
}

// A pool whose accounts never yield an injectable token must still be swept
// evenly: the per-tick budget may not be spent on the same few accounts forever.
func TestDueAccountsRotatesPastJustProbedAccounts(t *testing.T) {
	svc, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	cfg := svc.cachedConfig()
	ids := []int64{11, 12, 13}

	first := svc.dueAccounts(ids, cfg, now, nil)
	if len(first) != 3 {
		t.Fatalf("first pass = %v, want all three accounts", first)
	}

	// The first account was just probed, so the next pass must move on to the
	// others instead of picking it again.
	svc.markProbed(11)
	second := svc.dueAccounts(ids, cfg, now, nil)
	if len(second) != 2 || second[0] != 12 || second[1] != 13 {
		t.Fatalf("second pass = %v, want the remaining accounts", second)
	}
	if svc.dueAccounts(ids, cfg, now.Add(time.Second), nil)[0] == 11 {
		t.Error("an account inside its cooldown must not be picked again")
	}

	// Once the cooldown has passed it is eligible again.
	later := svc.dueAccounts(ids, cfg, now.Add(time.Duration(CodexTurnStateDefaultPollSeconds+1)*time.Second), nil)
	if len(later) != 3 {
		t.Fatalf("pass after the cooldown = %v, want all three again", later)
	}

	// Accounts the caller already forced out of the queue are skipped.
	skipped := svc.dueAccounts(ids, cfg, now.Add(time.Hour), map[int64]struct{}{11: {}})
	for _, id := range skipped {
		if id == 11 {
			t.Error("a forced account must not be picked twice in one pass")
		}
	}
}

func TestInjectCodexTurnStateHonoursForceMode(t *testing.T) {
	cases := []struct {
		name       string
		force      bool
		clientSent string
		want       string
	}{
		{"fill_empty leaves the client's own state", false, "client-state", "client-state"},
		{"fill_empty fills a blank header", false, "", "pooled-state"},
		{"force overwrites the client's state", true, "client-state", "pooled-state"},
		{"force fills a blank header", true, "", "pooled-state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{codexTurnState: &fakeTurnStateGateway{value: "pooled-state", force: tc.force}}
			request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.clientSent != "" {
				request.Header.Set(CodexTurnStateDefaultHeader, tc.clientSent)
			}
			svc.injectCodexTurnState(request, &Account{ID: 7})
			if got := request.Header.Get(CodexTurnStateDefaultHeader); got != tc.want {
				t.Errorf("header = %q, want %q", got, tc.want)
			}
		})
	}
}
