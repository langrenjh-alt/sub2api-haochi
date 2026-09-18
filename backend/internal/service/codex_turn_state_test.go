package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTurnStateRepo is an in-memory CodexTurnStateRepository. It records what the
// service asked it to store so the tests can assert on persistence decisions
// without a database.
type fakeTurnStateRepo struct {
	mu       sync.Mutex
	records  []CodexTurnStateRecord
	inserted int
	touched  int
	invalid  int
	expired  int
	cfg      CodexTurnStateConfig
	groups   map[int64]int64
}

func newFakeTurnStateRepo() *fakeTurnStateRepo {
	return &fakeTurnStateRepo{groups: map[int64]int64{}}
}

func (f *fakeTurnStateRepo) Config(context.Context) (CodexTurnStateConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return NormalizeCodexTurnStateConfig(f.cfg), nil
}

func (f *fakeTurnStateRepo) SaveConfig(_ context.Context, _ int64, cfg CodexTurnStateConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	return nil
}

func (f *fakeTurnStateRepo) LatestPerAccount(_ context.Context, accountIDs []int64) (map[int64]CodexTurnStateRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]CodexTurnStateRecord{}
	for _, id := range accountIDs {
		for i := len(f.records) - 1; i >= 0; i-- {
			if f.records[i].AccountID == id {
				out[id] = f.records[i]
				break
			}
		}
	}
	return out, nil
}

func (f *fakeTurnStateRepo) Insert(_ context.Context, record *CodexTurnStateRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record.ID = int64(len(f.records) + 1)
	f.records = append(f.records, *record)
	f.inserted++
	return record.ID, nil
}

func (f *fakeTurnStateRepo) Touch(_ context.Context, _ int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched++
	return nil
}

func (f *fakeTurnStateRepo) Invalidate(_ context.Context, accountID int64, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, CodexTurnStateRecord{
		ID:        int64(len(f.records) + 1),
		AccountID: accountID,
		Status:    CodexTurnStateStatusRevoked,
		Error:     note,
	})
	f.invalid++
	return nil
}

func (f *fakeTurnStateRepo) ExpireStale(context.Context, time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expired++
	return 0, nil
}

func (f *fakeTurnStateRepo) History(context.Context, int64, int, int) (*CodexTurnStateHistoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &CodexTurnStateHistoryPage{Total: int64(len(f.records))}, nil
}

func (f *fakeTurnStateRepo) CountersSince(context.Context, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

func (f *fakeTurnStateRepo) Prune(context.Context, int, time.Time) (int64, error) { return 0, nil }

func (f *fakeTurnStateRepo) EnrolledAccounts(context.Context, []int64) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int64]int64, len(f.groups))
	for id, groupID := range f.groups {
		out[id] = groupID
	}
	return out, nil
}

func (f *fakeTurnStateRepo) snapshot() []CodexTurnStateRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CodexTurnStateRecord, len(f.records))
	copy(out, f.records)
	return out
}

// fernetToken builds a token with the Fernet shape the decoder understands:
// version byte 0x80 followed by an 8-byte big-endian timestamp.
func fernetToken(t *testing.T, issued time.Time) string {
	t.Helper()
	raw := make([]byte, codexTurnStateFernetOverhead+12*16)
	raw[0] = 0x80
	seconds := uint64(issued.Unix())
	for i := 8; i >= 1; i-- {
		raw[i] = byte(seconds & 0xff)
		seconds >>= 8
	}
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(raw)
}

func TestDecodeFernetIssuedAtReadsEmbeddedTimestamp(t *testing.T) {
	issued := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	got := decodeFernetIssuedAt(fernetToken(t, issued))
	if !got.Equal(issued) {
		t.Fatalf("issued_at = %s, want %s", got, issued)
	}
}

func TestDecodeFernetIssuedAtRejectsUnusableTokens(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"not base64":     "!!!not-a-token!!!",
		"too short":      base64.URLEncoding.EncodeToString([]byte{0x80, 0x01}),
		"wrong version":  base64.URLEncoding.EncodeToString(append([]byte{0x81}, make([]byte, 24)...)),
		"zero timestamp": base64.URLEncoding.EncodeToString(append([]byte{0x80}, make([]byte, 24)...)),
	}
	for name, token := range cases {
		if got := decodeFernetIssuedAt(token); !got.IsZero() {
			t.Errorf("%s: got %s, want zero time", name, got)
		}
	}
}

func TestDecodeFernetIssuedAtRejectsAbsurdTimestamps(t *testing.T) {
	far := time.Now().UTC().Add(400 * 24 * time.Hour)
	if got := decodeFernetIssuedAt(fernetToken(t, far)); !got.IsZero() {
		t.Fatalf("future token accepted as %s", got)
	}
	ancient := time.Unix(1_000_000, 0).UTC()
	if got := decodeFernetIssuedAt(fernetToken(t, ancient)); !got.IsZero() {
		t.Fatalf("ancient token accepted as %s", got)
	}
}

func TestStateFingerprintIsStableAndDoesNotRevealTheToken(t *testing.T) {
	token := "gAAAAABsupersecrettoken"
	first := stateFingerprint(token)
	if first != stateFingerprint(token) {
		t.Fatal("fingerprint is not stable")
	}
	if first == stateFingerprint(token+"x") {
		t.Fatal("different tokens produced the same fingerprint")
	}
	if len(first) != 16 {
		t.Fatalf("fingerprint length = %d, want 16 hex chars", len(first))
	}
	if containsSecret(first, token) {
		t.Fatal("fingerprint leaked the token")
	}
}

func containsSecret(haystack, needle string) bool {
	if len(needle) == 0 {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestNormalizeCodexTurnStateConfigFillsDefaults(t *testing.T) {
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})
	if cfg.Model != CodexTurnStateDefaultModel {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.InjectHeader != "X-Codex-Turn-State" {
		t.Errorf("inject header = %q, want canonical form", cfg.InjectHeader)
	}
	if cfg.TTLSeconds != CodexTurnStateDefaultTTLSeconds {
		t.Errorf("ttl = %d", cfg.TTLSeconds)
	}
	if cfg.RenewBeforeSeconds != CodexTurnStateDefaultRenewBeforeSeconds {
		t.Errorf("renew before = %d", cfg.RenewBeforeSeconds)
	}
	if cfg.PollSeconds != CodexTurnStateDefaultPollSeconds {
		t.Errorf("poll = %d", cfg.PollSeconds)
	}
	if len(cfg.RevocationStatuses) != 0 {
		// 312 turned out to be a token length, not an HTTP status, so nothing may
		// be revoked by default (see the correction note on the constants).
		t.Errorf("revocation statuses = %v, want none by default", cfg.RevocationStatuses)
	}
	if cfg.InjectMode != CodexTurnStateInjectModeFillEmpty {
		t.Errorf("inject mode = %q, want the conservative default", cfg.InjectMode)
	}
	if cfg.AllowDegradedShapes {
		t.Error("degraded shapes must be refused by default")
	}
	if err := ValidateCodexTurnStateConfig(cfg); err != nil {
		t.Fatalf("normalized defaults must validate: %v", err)
	}
}

func TestValidateCodexTurnStateConfigRejectsBadInput(t *testing.T) {
	base := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})

	cases := []struct {
		name   string
		mutate func(*CodexTurnStateConfig)
	}{
		{"ttl too small", func(c *CodexTurnStateConfig) { c.TTLSeconds = 10 }},
		{"ttl too large", func(c *CodexTurnStateConfig) { c.TTLSeconds = 100000 }},
		{"renew not before ttl", func(c *CodexTurnStateConfig) { c.TTLSeconds = 600; c.RenewBeforeSeconds = 600 }},
		{"poll too small", func(c *CodexTurnStateConfig) { c.PollSeconds = 1 }},
		{"probe timeout too large", func(c *CodexTurnStateConfig) { c.ProbeTimeoutSeconds = 5000 }},
		{"max accounts too large", func(c *CodexTurnStateConfig) { c.MaxAccountsPerTick = 5000 }},
		{"invalid header token", func(c *CodexTurnStateConfig) { c.InjectHeader = "bad header name" }},
		{"revocation code out of range", func(c *CodexTurnStateConfig) { c.RevocationStatuses = []int{99} }},
		{"negative proxy", func(c *CodexTurnStateConfig) { c.ProxyID = -1 }},
	}
	for _, tc := range cases {
		cfg := base
		tc.mutate(&cfg)
		if err := ValidateCodexTurnStateConfig(cfg); err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
		}
	}
}

func TestValidateCodexTurnStateConfigRequiresTargetsWhenEnabled(t *testing.T) {
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true})
	if err := ValidateCodexTurnStateConfig(cfg); err == nil {
		t.Fatal("enabling without groups, accounts or proxy must be rejected")
	}
	cfg.GroupIDs = []int64{24}
	if err := ValidateCodexTurnStateConfig(cfg); err != nil {
		t.Fatalf("a configured group must satisfy the requirement: %v", err)
	}
}

func newTestTurnStateService(repo CodexTurnStateRepository, cfg CodexTurnStateConfig) *CodexTurnStateService {
	svc := NewCodexTurnStateService(repo, nil, nil, nil)
	svc.cfg = NormalizeCodexTurnStateConfig(cfg)
	svc.cfgAt = time.Now()
	return svc
}

func TestInjectionHeaderServesActiveStatesOnly(t *testing.T) {
	now := time.Now()
	repo := newFakeTurnStateRepo()
	svc := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})

	svc.storeCache(7, cachedTurnState{model: CodexTurnStateDefaultModel,
		state:     synthTurnState(12, now),
		expiresAt: now.Add(30 * time.Minute),
		recordID:  1,
	})
	name, value, ok := svc.InjectionHeader(7, "")
	if !ok {
		t.Fatal("an active state must be injected")
	}
	if name != "X-Codex-Turn-State" {
		t.Errorf("header name = %q", name)
	}
	if value != synthTurnState(12, now) {
		t.Errorf("header value = %q", value)
	}

	// An expired state must never be presented: upstream would only have to
	// replace it.
	svc.storeCache(8, cachedTurnState{model: CodexTurnStateDefaultModel, state: "stale", expiresAt: now.Add(-time.Second), recordID: 2})
	if _, _, ok := svc.InjectionHeader(8, ""); ok {
		t.Fatal("an expired state must not be injected")
	}

	// A revoked state is dropped rather than reused.
	svc.storeCache(9, cachedTurnState{model: CodexTurnStateDefaultModel, state: "revoked", expiresAt: now.Add(time.Hour), revoked: true, recordID: 3})
	if _, _, ok := svc.InjectionHeader(9, ""); ok {
		t.Fatal("a revoked state must not be injected")
	}

	// Unknown accounts are simply not enrolled.
	if _, _, ok := svc.InjectionHeader(999, ""); ok {
		t.Fatal("an account with no state must not be injected")
	}
}

func TestInjectionHeaderHonoursSwitches(t *testing.T) {
	now := time.Now()
	repo := newFakeTurnStateRepo()

	disabled := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: false, InjectEnabled: true})
	disabled.storeCache(7, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, now), expiresAt: now.Add(time.Hour), recordID: 1})
	if _, _, ok := disabled.InjectionHeader(7, ""); ok {
		t.Fatal("a disabled pool must not inject")
	}

	noInject := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: false})
	noInject.storeCache(7, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, now), expiresAt: now.Add(time.Hour), recordID: 1})
	if _, _, ok := noInject.InjectionHeader(7, ""); ok {
		t.Fatal("inject_enabled=false must not inject")
	}
}

func TestObserveResponseStoresHarvestedState(t *testing.T) {
	repo := newFakeTurnStateRepo()
	svc := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})

	issued := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	token := fernetToken(t, issued)
	header := http.Header{}
	header.Set("X-Codex-Turn-State", token)

	svc.persist(context.Background(), codexTurnStateEvent{
		accountID:  42,
		state:      token,
		statusCode: http.StatusOK,
		model:      "gpt-6-astra",
		source:     CodexTurnStateSourceHarvest,
	})

	records := repo.snapshot()
	if len(records) != 1 {
		t.Fatalf("stored %d records, want 1", len(records))
	}
	record := records[0]
	if record.Status != CodexTurnStateStatusActive {
		t.Errorf("status = %q, want active", record.Status)
	}
	if record.State != token {
		t.Error("raw token was not stored, so it could never be injected")
	}
	if record.StateFingerprint != stateFingerprint(token) {
		t.Error("fingerprint does not match the token")
	}
	if record.IssuedAt == nil || !record.IssuedAt.Equal(issued) {
		t.Errorf("issued_at = %v, want the timestamp embedded in the token", record.IssuedAt)
	}
	wantExpiry := issued.Add(time.Duration(CodexTurnStateDefaultTTLSeconds) * time.Second)
	if record.ExpiresAt == nil || !record.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expires_at = %v, want %v", record.ExpiresAt, wantExpiry)
	}

	// The cache must now serve the harvested state.
	if _, value, ok := svc.InjectionHeader(42, ""); !ok || value != token {
		t.Fatal("harvested state is not injectable")
	}
}

func TestPersistRefreshesInsteadOfDuplicatingTheSameState(t *testing.T) {
	repo := newFakeTurnStateRepo()
	svc := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})

	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	token := fernetToken(t, issued)
	event := codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 42, state: token, statusCode: 200, source: CodexTurnStateSourceHarvest}

	svc.persist(context.Background(), event)
	svc.persist(context.Background(), event)

	if got := repo.inserted; got != 1 {
		t.Fatalf("inserted %d rows for one token, want 1", got)
	}
	if repo.touched != 1 {
		t.Fatalf("touched %d rows, want 1", repo.touched)
	}
}

func TestPersistMarksAnAlreadyExpiredTokenExpired(t *testing.T) {
	repo := newFakeTurnStateRepo()
	cfg := CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}, TTLSeconds: 60}
	svc := newTestTurnStateService(repo, cfg)

	issued := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	token := fernetToken(t, issued)
	svc.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 42, state: token, statusCode: 200})

	records := repo.snapshot()
	if len(records) != 1 || records[0].Status != CodexTurnStateStatusExpired {
		t.Fatalf("records = %+v, want one expired row", records)
	}
	if _, _, ok := svc.InjectionHeader(42, ""); ok {
		t.Fatal("an expired token must not be injected")
	}
}

func TestRevokeDropsTheStateAndQueuesARefresh(t *testing.T) {
	repo := newFakeTurnStateRepo()
	svc := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})
	svc.storeCache(42, cachedTurnState{model: CodexTurnStateDefaultModel, state: "live", expiresAt: time.Now().Add(time.Hour), recordID: 1})

	svc.Revoke(42, CodexTurnStateDefaultRevocationStatus, "上游下发撤销状态")

	if _, _, ok := svc.InjectionHeader(42, ""); ok {
		t.Fatal("a revoked account must stop injecting immediately")
	}
	select {
	case id := <-svc.refresh:
		if id != 42 {
			t.Fatalf("queued refresh for %d, want 42", id)
		}
	default:
		t.Fatal("revocation must queue an immediate refresh")
	}

	// Draining the queue through persist records the revocation in history.
	svc.persist(context.Background(), <-svc.events)
	if repo.invalid != 1 {
		t.Fatalf("invalidations = %d, want 1", repo.invalid)
	}
	if records := repo.snapshot(); records[len(records)-1].Status != CodexTurnStateStatusRevoked {
		t.Fatal("revocation was not recorded in the timeline")
	}
}

func TestNeedsRefreshCoversTheRenewalWindow(t *testing.T) {
	now := time.Now()
	repo := newFakeTurnStateRepo()
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})
	svc := newTestTurnStateService(repo, cfg)

	if !svc.needsRefresh(1, cfg, now) {
		t.Fatal("an account with no state must be refreshed")
	}

	svc.storeCache(2, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, now), expiresAt: now.Add(time.Hour), recordID: 1})
	if svc.needsRefresh(2, cfg, now) {
		t.Fatal("a comfortably valid state must not be refreshed")
	}

	// Inside the renewal window it becomes due.
	inside := time.Duration(cfg.RenewBeforeSeconds-1) * time.Second
	svc.storeCache(3, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, now), expiresAt: now.Add(inside), recordID: 2})
	if !svc.needsRefresh(3, cfg, now) {
		t.Fatal("a state inside the renewal window must be refreshed")
	}

	svc.storeCache(4, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, now), expiresAt: now.Add(time.Hour), revoked: true, recordID: 3})
	if !svc.needsRefresh(4, cfg, now) {
		t.Fatal("a revoked state must be refreshed")
	}
}

func TestExtractCodexTurnStateFieldAcceptsFlatAndNestedShapes(t *testing.T) {
	flat := []byte(`{"current_turn_state":"abc123"}`)
	if got := extractCodexTurnStateField(flat); got != "abc123" {
		t.Errorf("flat shape = %q", got)
	}
	nested := []byte(`{"error":null,"data":{"meta":{"turn_state":"xyz789"}}}`)
	if got := extractCodexTurnStateField(nested); got != "xyz789" {
		t.Errorf("nested shape = %q", got)
	}
	if got := extractCodexTurnStateField([]byte(`{"unrelated":true}`)); got != "" {
		t.Errorf("unrelated body produced %q", got)
	}
	if got := extractCodexTurnStateField([]byte(`not json`)); got != "" {
		t.Errorf("invalid json produced %q", got)
	}
}

func TestIsCodexTurnStateUpstreamHostOnlyMatchesTheCodexBackend(t *testing.T) {
	for _, host := range []string{"chatgpt.com", "chatgpt.com:443", "CHATGPT.COM"} {
		if !isCodexTurnStateUpstreamHost(host) {
			t.Errorf("%s must be treated as the Codex backend", host)
		}
	}
	for _, host := range []string{"api.openai.com", "example.com", ""} {
		if isCodexTurnStateUpstreamHost(host) {
			t.Errorf("%s must not be treated as the Codex backend", host)
		}
	}
}

func TestGatewayInjectsOnlyOnTheCodexBackend(t *testing.T) {
	repo := newFakeTurnStateRepo()
	pool := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})
	pool.storeCache(42, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, time.Unix(1789716000, 0)), expiresAt: time.Now().Add(time.Hour), recordID: 1})

	gateway := &OpenAIGatewayService{codexTurnState: pool}
	account := &Account{ID: 42}

	codexReq, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
	gateway.injectCodexTurnState(codexReq, account)
	if got := codexReq.Header.Get("X-Codex-Turn-State"); got != synthTurnState(12, time.Unix(1789716000, 0)) {
		t.Fatalf("codex request header = %q, want the pooled state", got)
	}

	platformReq, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	gateway.injectCodexTurnState(platformReq, account)
	if got := platformReq.Header.Get("X-Codex-Turn-State"); got != "" {
		t.Fatalf("platform request must not carry a Codex state, got %q", got)
	}
}

func TestGatewayWithoutPoolIsInert(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
	gateway.injectCodexTurnState(request, &Account{ID: 1})
	gateway.observeCodexTurnState(&http.Response{StatusCode: 200, Header: http.Header{}}, &Account{ID: 1})
	if got := request.Header.Get("X-Codex-Turn-State"); got != "" {
		t.Fatalf("a nil pool injected %q", got)
	}
}

func TestBuildCodexTurnStateProbePayloadCarriesTheEngineFingerprint(t *testing.T) {
	payload := buildCodexTurnStateProbePayload("gpt-6-astra", "只回答整数：21")
	if payload["model"] != "gpt-6-astra" {
		t.Errorf("model = %v", payload["model"])
	}
	if payload["stream"] != true {
		t.Error("the probe must stream so the state is emitted with the headers")
	}
	if payload["store"] != false {
		t.Error("OAuth Codex requests require store=false")
	}
	meta, ok := payload["client_metadata"].(map[string]string)
	if !ok {
		t.Fatalf("client_metadata = %T, want map[string]string", payload["client_metadata"])
	}
	if meta["x-codex-window-id"] == "" || meta["x-codex-installation-id"] == "" {
		t.Fatal("client_metadata is missing the engine-fingerprint signals")
	}
}

func TestBuildCodexTurnStateProbePayloadFallsBackToTheDefaultPrompt(t *testing.T) {
	payload := buildCodexTurnStateProbePayload("gpt-6-astra", "   ")
	input, ok := payload["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %T", payload["input"])
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %T", input[0]["content"])
	}
	if content[0]["text"] != CodexTurnStateDefaultPrompt {
		t.Errorf("text = %v, want the default prompt", content[0]["text"])
	}
}

func TestIssuanceStatusGateDefaultsToAcceptingAnyState(t *testing.T) {
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})
	if len(cfg.IssuanceStatuses) != 0 {
		t.Fatalf("issuance statuses = %v, want empty by default", cfg.IssuanceStatuses)
	}
	// The observed reality: a normal 200 carries the state and it is honoured.
	for _, code := range []int{200, 292} {
		if !cfg.AcceptsIssuanceStatus(code) {
			t.Errorf("status %d must be accepted by default", code)
		}
	}
}

func TestIssuanceStatusGateCanRequireThePassIssuanceStatus(t *testing.T) {
	// The strict reading of the write-up: only a 292 may introduce a state.
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{
		Enabled: true, GroupIDs: []int64{24}, IssuanceStatuses: []int{292},
	})
	if cfg.AcceptsIssuanceStatus(200) {
		t.Fatal("a 200 must not introduce a state when only 292 is trusted")
	}
	if !cfg.AcceptsIssuanceStatus(292) {
		t.Fatal("a 292 must introduce a state in strict mode")
	}

	// A 200 that carries a state is then ignored entirely: nothing is stored and
	// nothing becomes injectable.
	repo := newFakeTurnStateRepo()
	svc := newTestTurnStateService(repo, cfg)
	header := http.Header{}
	header.Set("X-Codex-Turn-State", "gAAAAABfromaplain200")
	svc.ObserveResponse(42, header, http.StatusOK, "gpt-6-astra")
	svc.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 42, state: "gAAAAABfromaplain200", statusCode: 200})

	if got := len(repo.snapshot()); got != 0 {
		t.Fatalf("strict mode stored %d rows from a 200, want 0", got)
	}
	if _, _, ok := svc.InjectionHeader(42, ""); ok {
		t.Fatal("strict mode must not inject a state that a 200 introduced")
	}
}

func TestNormalizeKeepsIssuanceStatusesAndValidationRejectsBadOnes(t *testing.T) {
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{IssuanceStatuses: []int{292, 292, 0, 999}})
	if len(cfg.IssuanceStatuses) != 1 || cfg.IssuanceStatuses[0] != 292 {
		t.Fatalf("issuance statuses = %v, want [292]", cfg.IssuanceStatuses)
	}
	bad := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})
	bad.IssuanceStatuses = []int{99}
	if err := ValidateCodexTurnStateConfig(bad); err == nil {
		t.Fatal("an out-of-range issuance status must be rejected")
	}
}

func TestGatewayDoesNotOverwriteAClientEchoedTurnState(t *testing.T) {
	// A real Codex client captures the blob from its own response and echoes it
	// for the rest of the turn. That value must survive: overwriting it with a
	// pooled token would be the cross-account contradiction the echo guard
	// exists to prevent.
	repo := newFakeTurnStateRepo()
	pool := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})
	pool.storeCache(42, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, time.Unix(1789716000, 0)), expiresAt: time.Now().Add(time.Hour), recordID: 1})

	gateway := &OpenAIGatewayService{codexTurnState: pool}
	request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
	request.Header.Set(openAICodexTurnStateHeader, "client-own-token")

	gateway.injectCodexTurnState(request, &Account{ID: 42})

	if got := request.Header.Get(openAICodexTurnStateHeader); got != "client-own-token" {
		t.Fatalf("turn state = %q, want the client's own value preserved", got)
	}
}

func TestGatewayFillsAnEmptyTurnStateSlot(t *testing.T) {
	// The echo guard strips a foreign-account blob, leaving an empty slot; that
	// slot is exactly what the pool should fill with this account's own state.
	repo := newFakeTurnStateRepo()
	pool := newTestTurnStateService(repo, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})
	pool.storeCache(42, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(12, time.Unix(1789716000, 0)), expiresAt: time.Now().Add(time.Hour), recordID: 1})

	gateway := &OpenAIGatewayService{codexTurnState: pool}
	request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
	request.Header.Set(openAICodexTurnStateHeader, "   ")

	gateway.injectCodexTurnState(request, &Account{ID: 42})

	if got := request.Header.Get(openAICodexTurnStateHeader); got != synthTurnState(12, time.Unix(1789716000, 0)) {
		t.Fatalf("turn state = %q, want the pooled value", got)
	}
}
