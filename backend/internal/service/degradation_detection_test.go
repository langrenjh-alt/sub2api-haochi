package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsDegradationTestType(t *testing.T) {
	for _, testType := range []string{DegradationTestTypeProbe, DegradationTestTypePreview} {
		if !IsDegradationTestType(testType) {
			t.Fatalf("%s must be a degradation test type", testType)
		}
	}
	if IsDegradationTestType("candy") || IsDegradationTestType("") {
		t.Fatal("unrelated test types must not be treated as degradation probes")
	}
}

func TestNormalizeDegradationConfigDefaults(t *testing.T) {
	cfg := NormalizeDegradationConfig(DegradationDetectionConfig{})
	if cfg.IntervalMinute != 10 {
		t.Fatalf("interval default = %d, want 10", cfg.IntervalMinute)
	}
	if cfg.Model != "gpt-6-astra" {
		t.Fatalf("model default = %q, want gpt-6-astra", cfg.Model)
	}
	if cfg.ReasoningEffort != "medium" {
		t.Fatalf("probe effort default = %q, want medium", cfg.ReasoningEffort)
	}
	if cfg.PreviewReasoningEffort != "low" {
		t.Fatalf("preview effort default = %q, want low", cfg.PreviewReasoningEffort)
	}
	if cfg.ExpectedAnswer != DegradationExpectedAnswer {
		t.Fatalf("expected answer default = %q, want %s", cfg.ExpectedAnswer, DegradationExpectedAnswer)
	}
	if cfg.SuspendMinute != 30 {
		t.Fatalf("suspend default = %d, want 30", cfg.SuspendMinute)
	}
	if cfg.PreviewIntervalMinute != 10 {
		t.Fatalf("preview interval default = %d, want 10", cfg.PreviewIntervalMinute)
	}
	if err := ValidateDegradationConfig(cfg); err != nil {
		t.Fatalf("normalized defaults must validate: %v", err)
	}
}

func TestNormalizeDegradationConfigCanonicalizesEffort(t *testing.T) {
	cfg := NormalizeDegradationConfig(DegradationDetectionConfig{ReasoningEffort: " Extra_High "})
	if cfg.ReasoningEffort != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", cfg.ReasoningEffort)
	}
}

func TestValidateDegradationConfigRejectsBadInput(t *testing.T) {
	base := NormalizeDegradationConfig(DegradationDetectionConfig{})
	cases := map[string]func(c *DegradationDetectionConfig){
		"interval negative":    func(c *DegradationDetectionConfig) { c.IntervalMinute = -5 },
		"interval too large":   func(c *DegradationDetectionConfig) { c.IntervalMinute = 1441 },
		"suspend too large":    func(c *DegradationDetectionConfig) { c.SuspendMinute = 1441 },
		"unsupported effort":   func(c *DegradationDetectionConfig) { c.ReasoningEffort = "turbo" },
		"preview effort empty": func(c *DegradationDetectionConfig) { c.PreviewReasoningEffort = "nope" },
		"timeout too small":    func(c *DegradationDetectionConfig) { c.TimeoutSeconds = 5 },
		"image model":          func(c *DegradationDetectionConfig) { c.Model = "gpt-image-2" },
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if err := ValidateDegradationConfig(cfg); err == nil {
			t.Fatalf("%s: expected a validation error, got nil", name)
		}
	}
}

// An empty payload means "use the defaults"; a value that was actually supplied
// must be rejected instead of being silently rewritten.
func TestValidateDegradationConfigTreatsEmptyAsUnset(t *testing.T) {
	if err := ValidateDegradationConfig(DegradationDetectionConfig{}); err != nil {
		t.Fatalf("an empty payload must be accepted as defaults: %v", err)
	}
	if err := ValidateDegradationConfig(DegradationDetectionConfig{IntervalMinute: -5}); err == nil {
		t.Fatal("a supplied but negative interval must be rejected")
	}
	if err := ValidateDegradationConfig(DegradationDetectionConfig{ReasoningEffort: "turbo"}); err == nil {
		t.Fatal("a supplied but unsupported effort must be rejected")
	}
	// The two-pass contract the service uses: validate raw, normalize, validate.
	raw := DegradationDetectionConfig{Enabled: true, IntervalMinute: 30, ReasoningEffort: "Extra_High"}
	if err := ValidateDegradationConfig(raw); err != nil {
		t.Fatalf("a valid raw payload must pass: %v", err)
	}
	normalized := NormalizeDegradationConfig(raw)
	if normalized.ReasoningEffort != "xhigh" {
		t.Fatalf("normalized effort = %q, want xhigh", normalized.ReasoningEffort)
	}
	if err := ValidateDegradationConfig(normalized); err != nil {
		t.Fatalf("the normalized payload must pass: %v", err)
	}
}

// The pinned effort must land on whichever request shape the protocol adapter
// built, and must be a no-op when the configuration leaves it empty.
func TestApplyIntelligentPayloadEffortShapes(t *testing.T) {
	chat := map[string]any{"model": "gpt-6-astra", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	applyIntelligentPayloadEffort(chat, "medium")
	if chat["reasoning_effort"] != "medium" {
		t.Fatalf("chat completions payload must carry reasoning_effort=medium, got %v", chat["reasoning_effort"])
	}

	responses := map[string]any{"model": "gpt-6-astra", "input": []map[string]any{}}
	applyIntelligentPayloadEffort(responses, "low")
	nested, ok := responses["reasoning"].(map[string]any)
	if !ok || nested["effort"] != "low" {
		t.Fatalf("responses payload must carry reasoning.effort=low, got %v", responses["reasoning"])
	}

	anthropic := map[string]any{"model": "claude", "output_config": map[string]any{}}
	applyIntelligentPayloadEffort(anthropic, "high")
	if anthropic["output_config"].(map[string]any)["effort"] != "high" {
		t.Fatalf("anthropic payload must carry output_config.effort=high, got %v", anthropic["output_config"])
	}

	untouched := map[string]any{"model": "gpt-6-astra", "messages": []map[string]any{}}
	applyIntelligentPayloadEffort(untouched, "")
	if _, exists := untouched["reasoning_effort"]; exists {
		t.Fatal("an empty effort must not add a reasoning field")
	}
}

// applyIntelligentPayloadPrompt is the single hook the protocol adapters call.
// It must keep rewriting the prompt exactly as before and additionally pin the
// configured thinking level.
func TestApplyIntelligentPayloadPromptPinsPromptAndEffort(t *testing.T) {
	ctx := context.WithValue(context.Background(), intelligentRunKey{}, &intelligentRunContext{
		prompt: DegradationCandyPrompt,
		effort: "medium",
	})
	payload := map[string]any{"model": "gpt-6-astra", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	applyIntelligentPayloadPrompt(ctx, payload)

	// The adapter rewrites messages into a text content block, so assert on the
	// block that now carries the probe question.
	messages := payload["messages"].([]map[string]any)
	blocks, ok := messages[0]["content"].([]map[string]any)
	if !ok || len(blocks) != 1 || blocks[0]["text"] != DegradationCandyPrompt {
		t.Fatalf("probe prompt must replace the connectivity test prompt, got %#v", messages[0]["content"])
	}
	if payload["reasoning_effort"] != "medium" {
		t.Fatalf("probe effort missing, got %v", payload["reasoning_effort"])
	}

	// A plain connectivity test (no intelligent run context) stays untouched.
	empty := map[string]any{"model": "gpt-6-astra", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	applyIntelligentPayloadPrompt(context.Background(), empty)
	if empty["messages"].([]map[string]any)[0]["content"] != "hi" {
		t.Fatal("non-probe payloads must not be rewritten")
	}
	if _, exists := empty["reasoning_effort"]; exists {
		t.Fatal("non-probe payloads must not gain a reasoning field")
	}
}

// The detector suspends an account through the official temporary-unschedulable
// field and then has to keep probing it. Only that cooldown may be skipped.
func TestAccountTestCooldownSkipsOnlyDetectorSuspension(t *testing.T) {
	suspended := time.Now().Add(20 * time.Minute)
	rateLimited := time.Now().Add(5 * time.Minute)
	account := &Account{
		ID:                      7,
		TempUnschedulableUntil:  &suspended,
		RateLimitResetAt:        &rateLimited,
		TempUnschedulableReason: DegradationSuspendReasonPrefix + "：暂停调度",
	}

	if err := accountTestCooldown(context.Background(), account, "gpt-6-astra", time.Now()); err == nil {
		t.Fatal("without the bypass the suspension must delay the probe")
	}

	ctx := withIntelligentTempSuspensionIgnored(context.Background())
	err := accountTestCooldown(ctx, account, "gpt-6-astra", time.Now())
	if err == nil {
		t.Fatal("a rate-limit cooldown must still delay the probe")
	}
	var wait *TestAdmissionWaitError
	if !errors.As(err, &wait) {
		t.Fatalf("unexpected error: %v", err)
	}
	if wait.Reason != "账号限流冷却尚未结束" {
		t.Fatalf("bypass leaked to the wrong cooldown: %s", wait.Reason)
	}

	account.RateLimitResetAt = nil
	if err := accountTestCooldown(ctx, account, "gpt-6-astra", time.Now()); err != nil {
		t.Fatalf("the detector's own suspension must not delay its re-check: %v", err)
	}
}

// The candy question is the whole verdict: 21 is healthy, any other readable
// integer is degraded, and an unreadable answer changes nothing.
func TestDegradationProbeVerdicts(t *testing.T) {
	cfg := NormalizeDegradationConfig(DegradationDetectionConfig{})
	config := IntelligentTestConfig{
		Prompt:         DegradationCandyPrompt,
		Model:          cfg.Model,
		Evaluator:      "exact_answer",
		ExpectedAnswer: cfg.ExpectedAnswer,
		AnswerType:     "number",
		AnswerFormat:   "free_text",
		AnswerUnitMode: "none",
	}
	config.TimeoutSeconds = cfg.TimeoutSeconds

	for _, tc := range []struct {
		name    string
		output  string
		verdict string
	}{
		{"healthy", "21", "correct"},
		{"degraded", "22", "incorrect"},
		{"verbose but unambiguous", "22", "incorrect"},
		{"empty", "", "undetermined"},
	} {
		result := exactAnswerEvaluator{}.Evaluate(tc.output, config)
		verdict, _ := result.Detail["answer_verdict"].(string)
		if verdict != tc.verdict {
			t.Fatalf("%s: verdict = %q, want %q (detail=%v)", tc.name, verdict, tc.verdict, result.Detail)
		}
	}
}

// --------------------------------------------------------------- fake repo

type fakeDegradationRepo struct {
	works           *DegradationWorkPage
	timeline        *DegradationTimeline
	deletedWorks    []int64
	purged          bool
	purgedCount     int64
	groups          []DegradationGroup
	dueProbeIDs     []int64
	previewGroupIDs []int64
	duePreviewIDs   []int64
	enqueued        []enqueuedTest
	applied         []appliedOutcome
	cfg             DegradationDetectionConfig
	groupID         int64
	cfgError        error
}

type enqueuedTest struct {
	accountID int64
	testType  string
	model     string
	effort    string
	expected  string
}

type appliedOutcome struct {
	accountID int64
	degraded  bool
	minutes   int
	note      string
}

func (f *fakeDegradationRepo) Groups(context.Context) ([]DegradationGroup, error) {
	return f.groups, nil
}
func (f *fakeDegradationRepo) UpdateGroupConfig(context.Context, int64, int64, DegradationDetectionConfig) error {
	return nil
}
func (f *fakeDegradationRepo) SetTestSettingEnabled(context.Context, string, bool, bool) error {
	return nil
}
func (f *fakeDegradationRepo) PendingQueueDepth(context.Context) (int, error) { return 0, nil }
func (f *fakeDegradationRepo) DueProbeAccountIDs(context.Context, int64, int, int) ([]int64, error) {
	return f.dueProbeIDs, nil
}
func (f *fakeDegradationRepo) DuePreviewAccountIDs(context.Context, []int64, int, int) ([]int64, error) {
	return f.duePreviewIDs, nil
}
func (f *fakeDegradationRepo) EnqueueDegradationTest(_ context.Context, accountID int64, testType, _ string, model, effort, expected string, _ int) (int64, bool, error) {
	f.enqueued = append(f.enqueued, enqueuedTest{accountID: accountID, testType: testType, model: model, effort: effort, expected: expected})
	return int64(len(f.enqueued)), true, nil
}
func (f *fakeDegradationRepo) ApplyProbeOutcome(_ context.Context, accountID int64, degraded bool, minutes int, note string) (bool, error) {
	f.applied = append(f.applied, appliedOutcome{accountID, degraded, minutes, note})
	return true, nil
}
func (f *fakeDegradationRepo) Overview(context.Context, int) (*DegradationOverview, error) {
	return &DegradationOverview{}, nil
}
func (f *fakeDegradationRepo) GroupConfig(context.Context, int64) (DegradationDetectionConfig, error) {
	return f.cfg, nil
}
func (f *fakeDegradationRepo) AccountConfig(context.Context, int64) (int64, DegradationDetectionConfig, error) {
	return f.groupID, f.cfg, f.cfgError
}
func (f *fakeDegradationRepo) PreviewGroups(context.Context) ([]int64, error) {
	return f.previewGroupIDs, nil
}
func (f *fakeDegradationRepo) PublicPage(context.Context, int, int) (*DegradationPublicPage, error) {
	return &DegradationPublicPage{}, nil
}

func (f *fakeDegradationRepo) Timeline(context.Context, int) (*DegradationTimeline, error) {
	if f.timeline != nil {
		return f.timeline, nil
	}
	return &DegradationTimeline{}, nil
}

func (f *fakeDegradationRepo) Works(context.Context, int, int) (*DegradationWorkPage, error) {
	if f.works != nil {
		return f.works, nil
	}
	return &DegradationWorkPage{}, nil
}

func (f *fakeDegradationRepo) DeleteWork(_ context.Context, id int64) (bool, error) {
	f.deletedWorks = append(f.deletedWorks, id)
	return id != 404, nil
}

func (f *fakeDegradationRepo) PurgeWorks(context.Context) (int64, error) {
	f.purged = true
	return f.purgedCount, nil
}
func (f *fakeDegradationRepo) PublicWork(context.Context, int64) (*DegradationPublicWork, error) {
	return nil, ErrDegradationWorkNotFound
}

func probeRecord(accountID int64, verdict string, detail map[string]any) *IntelligentTestRecord {
	evaluation := map[string]any{"answer_verdict": verdict}
	for key, value := range detail {
		evaluation[key] = value
	}
	return &IntelligentTestRecord{ID: 1, AccountID: accountID, TestType: DegradationTestTypeProbe, Evaluation: evaluation}
}

func TestHandleIntelligentTestOutcomeSuspendsAndRecovers(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: NormalizeDegradationConfig(DegradationDetectionConfig{})}
	svc := NewDegradationService(repo)

	// A wrong answer suspends through the official window and tags the note.
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "incorrect", map[string]any{"normalized_answer": "22"}))
	if len(repo.applied) != 1 {
		t.Fatalf("expected one outcome write, got %d", len(repo.applied))
	}
	suspended := repo.applied[0]
	if !suspended.degraded || suspended.accountID != 5 {
		t.Fatalf("unexpected suspension: %+v", suspended)
	}
	if suspended.minutes != 30 {
		t.Fatalf("suspend minutes = %d, want 30", suspended.minutes)
	}
	if !strings.HasPrefix(suspended.note, DegradationSuspendReasonPrefix) {
		t.Fatalf("note must carry the detector marker, got %q", suspended.note)
	}
	if !strings.Contains(suspended.note, "22") {
		t.Fatalf("note must record the observed answer, got %q", suspended.note)
	}

	// The note quotes the number the verdict was graded against, so the default
	// configuration must not leak a different value into the reason text.
	if !strings.Contains(suspended.note, "应为 "+DegradationExpectedAnswer) {
		t.Fatalf("note must quote the graded expectation, got %q", suspended.note)
	}

	// A correct answer lifts it again.
	repo.applied = nil
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "correct", nil))
	if len(repo.applied) != 1 || repo.applied[0].degraded {
		t.Fatalf("expected one recovery write, got %+v", repo.applied)
	}
}

func TestHandleIntelligentTestOutcomeIgnoresAmbiguousResults(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: NormalizeDegradationConfig(DegradationDetectionConfig{})}
	svc := NewDegradationService(repo)

	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "undetermined", nil))
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "not_evaluated", nil))
	svc.HandleIntelligentTestOutcome(context.Background(), &IntelligentTestRecord{ID: 2, AccountID: 5, TestType: DegradationTestTypePreview, Evaluation: map[string]any{"answer_verdict": "incorrect"}})
	svc.HandleIntelligentTestOutcome(context.Background(), nil)

	if len(repo.applied) != 0 {
		t.Fatalf("ambiguous or unrelated results must never change scheduling, got %+v", repo.applied)
	}
}

func TestRunNowSkipsDisabledGroups(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: NormalizeDegradationConfig(DegradationDetectionConfig{})}
	svc := NewDegradationService(repo)
	queued, err := svc.RunNow(context.Background(), 0)
	if err != nil {
		t.Fatalf("RunNow returned an error: %v", err)
	}
	if queued != 0 {
		t.Fatalf("no groups means no queued probes, got %d", queued)
	}
}

// The group config is the single source of truth for a probe: whatever answer
// an operator saves there is what the queued row is graded against, and the
// artwork job never carries an answer at all.
func TestEnqueuedTestsCarryTheGroupConfiguration(t *testing.T) {
	cfg := NormalizeDegradationConfig(DegradationDetectionConfig{
		Enabled: true, PreviewEnabled: true, ExpectedAnswer: "29",
		Model: "gpt-6-astra", ReasoningEffort: "high",
		PreviewModel: "gpt-6-astra", PreviewReasoningEffort: "low",
	})
	if cfg.ExpectedAnswer != "29" {
		t.Fatalf("the normalizer must keep an explicit answer, got %q", cfg.ExpectedAnswer)
	}
	repo := &fakeDegradationRepo{
		cfg:             cfg,
		groups:          []DegradationGroup{{GroupID: 42, GroupName: "prod", Config: cfg}},
		dueProbeIDs:     []int64{7, 8},
		previewGroupIDs: []int64{42},
		duePreviewIDs:   []int64{9},
	}
	svc := NewDegradationService(repo)
	queued, err := svc.RunNow(context.Background(), 0)
	if err != nil {
		t.Fatalf("RunNow returned an error: %v", err)
	}
	if queued != 2 || len(repo.enqueued) != 2 {
		t.Fatalf("queued = %d with %d rows, want 2 and 2", queued, len(repo.enqueued))
	}
	for _, row := range repo.enqueued {
		if row.testType != DegradationTestTypeProbe || row.expected != "29" || row.model != "gpt-6-astra" || row.effort != "high" {
			t.Fatalf("probe row lost the group config: %+v", row)
		}
	}

	// The artwork job is the only other producer, and it must not pass a number.
	svc.Tick(context.Background())
	var previews int
	for _, row := range repo.enqueued {
		if row.testType != DegradationTestTypePreview {
			continue
		}
		previews++
		if row.expected != "" || row.effort != "low" {
			t.Fatalf("artwork row must stay answerless at low effort: %+v", row)
		}
	}
	if previews != 1 {
		t.Fatalf("expected exactly one artwork row, got %d", previews)
	}
}

// A slow probe can outlive an edit of the group config. The reason text must
// keep quoting the number the answer was graded against, otherwise the page
// shows "答案 29（应为 29）" and reads as a contradiction.
func TestSuspendNoteQuotesTheGradedExpectation(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: NormalizeDegradationConfig(DegradationDetectionConfig{ExpectedAnswer: "21"})}
	svc := NewDegradationService(repo)
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "incorrect", map[string]any{
		"normalized_answer": "29", "expected_answer": "29", "actual_answer": "29",
	}))
	if len(repo.applied) != 1 {
		t.Fatalf("expected one outcome write, got %d", len(repo.applied))
	}
	note := repo.applied[0].note
	if !strings.Contains(note, "应为 29") || strings.Contains(note, "应为 21") {
		t.Fatalf("note must quote the graded expectation, got %q", note)
	}

	// An old row without the field still falls back to the live group config.
	repo.applied = nil
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "incorrect", map[string]any{"normalized_answer": "22"}))
	if len(repo.applied) != 1 || !strings.Contains(repo.applied[0].note, "应为 21") {
		t.Fatalf("legacy rows must fall back to the group config: %+v", repo.applied)
	}
}

func TestSafePublicSVGDropsUnrenderableContent(t *testing.T) {
	if safePublicSVG("") != "" {
		t.Fatal("empty input must stay empty")
	}
	if safePublicSVG("<svg><script>alert(1)</script></svg>") != "" {
		t.Fatal("script content must never survive the public pipeline")
	}
	if safePublicSVG("not svg at all") != "" {
		t.Fatal("non-SVG input must be dropped")
	}
}

// The public page is operator-curated, so the panel pages through what it renders
// and deletes entries. A delete must never reach storage with a nonsense id.
func TestPublicWorkCurationPagingAndDeletion(t *testing.T) {
	repo := &fakeDegradationRepo{works: &DegradationWorkPage{Total: 2, Items: []DegradationPublicWork{{ID: 7, HasImage: true}}}, purgedCount: 3}
	svc := NewDegradationService(repo)

	page, err := svc.Works(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("Works returned an error: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 1 || !page.Items[0].HasImage {
		t.Fatalf("Works lost the artwork list: %+v", page)
	}

	if _, err := svc.DeleteWork(context.Background(), 0); !errors.Is(err, ErrDegradationWorkNotFound) {
		t.Fatalf("a zero identifier must be rejected before touching storage, got %v", err)
	}
	if len(repo.deletedWorks) != 0 {
		t.Fatalf("rejected identifiers must not reach the repository: %v", repo.deletedWorks)
	}
	deleted, err := svc.DeleteWork(context.Background(), 12)
	if err != nil || !deleted || len(repo.deletedWorks) != 1 || repo.deletedWorks[0] != 12 {
		t.Fatalf("delete did not forward the identifier: deleted=%v err=%v calls=%v", deleted, err, repo.deletedWorks)
	}

	removed, err := svc.PurgeWorks(context.Background())
	if err != nil || removed != 3 || !repo.purged {
		t.Fatalf("purge result lost: removed=%d err=%v called=%v", removed, err, repo.purged)
	}

	timeline, err := svc.Timeline(context.Background(), 6)
	if err != nil || timeline == nil {
		t.Fatalf("Timeline must always answer: %v %+v", err, timeline)
	}
}
