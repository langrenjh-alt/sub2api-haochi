package service

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Degradation detection reuses the existing intelligent-test plumbing: the two
// test types live in test_settings / account_tests, results are produced by the
// AccountTestService runner, and only the scheduling translation is new.
const (
	// DegradationTestTypeProbe answers the candy question. The expected answer
	// is a fixed integer, so any other value means the account is degraded.
	DegradationTestTypeProbe = "degradation_probe"
	// DegradationTestTypePreview draws the pelican SVG shown on the public page.
	DegradationTestTypePreview = "degradation_preview"
)

// DegradationExpectedAnswer is the candy problem's correct total. It is only a
// default: a group may override it in its own degradation_detection_config,
// and that override is the number both the runner and the suspend note use.
const DegradationExpectedAnswer = "21"

const (
	DegradationDefaultModel          = "gpt-6-astra"
	DegradationProbeDefaultEffort    = "medium"
	DegradationPreviewDefaultEffort  = "low"
	DegradationDefaultIntervalMinute = 10
	DegradationDefaultSuspendMinute  = 30
	DegradationDefaultTimeoutSeconds = 300

	degradationMinIntervalMinute = 1
	degradationMaxIntervalMinute = 1440
	degradationMinSuspendMinute  = 1
	degradationMaxSuspendMinute  = 1440
	degradationMaxPromptRunes    = 16000
)

// degradationSuspendReasonPrefix tags the account's temporary-unschedulable
// window as ours. Recovery only clears a pause whose reason still carries the
// prefix, so an unrelated pause (manual disable, overload, expiry) is never
// lifted by the detector.
const DegradationSuspendReasonPrefix = "降智检测"

// DegradationCandyPrompt is the canonical probe question. It is duplicated as a
// test_settings default by migration 241; that row only seeds the value, group
// overrides and this constant are the fallbacks.
const DegradationCandyPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）  形状 | 苹果味 | 桃子味 | 西瓜味 圆形 | 7 | 9 | 8 五角星形 | 7 | 6 | 4  不许联网，自己计算。只回答最少取出的糖果总数，使用一个整数，不要解释。`

// DegradationPelicanPrompt asks for the public page artwork, including the same
// candy question spoken by the pelican.
const DegradationPelicanPrompt = `做一张精细的SVG图片，内容是鹈鹕骑着自行车在孙悟空开的大型飞机机翼上骑行，
鹈鹕展开翅膀，上面托着秦始皇的北极熊，秦始皇骑在熊上打螺丝。鹈鹕喊出
“在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4”的结果数值  `

// IsDegradationTestType reports whether a test type belongs to the detector.
func IsDegradationTestType(testType string) bool {
	switch strings.TrimSpace(testType) {
	case DegradationTestTypeProbe, DegradationTestTypePreview:
		return true
	default:
		return false
	}
}

// degradationEvaluator returns the evaluator registered for a probe kind.
func degradationEvaluator(testType string) string {
	if strings.TrimSpace(testType) == DegradationTestTypePreview {
		return "svg_structure"
	}
	return "exact_answer"
}

type intelligentIgnoreTempSuspensionKey struct{}

// withIntelligentTempSuspensionIgnored marks a run that must observe an account
// regardless of the detector's own temporary suspension. It never bypasses
// rate-limit or overload cooldowns, and it never changes account state.
func withIntelligentTempSuspensionIgnored(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, intelligentIgnoreTempSuspensionKey{}, true)
}

func intelligentTempSuspensionIgnored(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ignored, _ := ctx.Value(intelligentIgnoreTempSuspensionKey{}).(bool)
	return ignored
}

// DegradationDetectionConfig is the per-group configuration. Zero values are
// filled by NormalizeDegradationConfig, so an empty JSONB object is a valid
// "enabled with defaults" payload.
type DegradationDetectionConfig struct {
	Enabled                bool   `json:"enabled"`
	IntervalMinute         int    `json:"interval_minutes"`
	Model                  string `json:"model"`
	ReasoningEffort        string `json:"reasoning_effort"`
	ExpectedAnswer         string `json:"expected_answer,omitempty"`
	Prompt                 string `json:"prompt,omitempty"`
	TimeoutSeconds         int    `json:"timeout_seconds"`
	SuspendMinute          int    `json:"suspend_minutes"`
	PreviewEnabled         bool   `json:"preview_enabled"`
	PreviewIntervalMinute  int    `json:"preview_interval_minutes"`
	PreviewModel           string `json:"preview_model"`
	PreviewReasoningEffort string `json:"preview_reasoning_effort"`
}

// NormalizeDegradationConfig fills defaults and canonicalizes valid values.
// Invalid values are rejected by ValidateDegradationConfig, not silently fixed.
func NormalizeDegradationConfig(cfg DegradationDetectionConfig) DegradationDetectionConfig {
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = DegradationDefaultModel
	}
	if cfg.IntervalMinute == 0 {
		cfg.IntervalMinute = DegradationDefaultIntervalMinute
	}
	if cfg.SuspendMinute == 0 {
		cfg.SuspendMinute = DegradationDefaultSuspendMinute
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = DegradationDefaultTimeoutSeconds
	}
	if cfg.ReasoningEffort = NormalizeMaxReasoningEffort(cfg.ReasoningEffort); cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = DegradationProbeDefaultEffort
	}
	if cfg.ExpectedAnswer = strings.TrimSpace(cfg.ExpectedAnswer); cfg.ExpectedAnswer == "" {
		cfg.ExpectedAnswer = DegradationExpectedAnswer
	}
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	cfg.PreviewModel = strings.TrimSpace(cfg.PreviewModel)
	if cfg.PreviewModel == "" {
		cfg.PreviewModel = DegradationDefaultModel
	}
	if cfg.PreviewIntervalMinute == 0 {
		cfg.PreviewIntervalMinute = DegradationDefaultIntervalMinute
	}
	if cfg.PreviewReasoningEffort = NormalizeMaxReasoningEffort(cfg.PreviewReasoningEffort); cfg.PreviewReasoningEffort == "" {
		cfg.PreviewReasoningEffort = DegradationPreviewDefaultEffort
	}
	return cfg
}

// ValidateDegradationConfig rejects operator input that is present but invalid.
//
// A zero number or an empty string means "leave it to the defaults" and is
// accepted; a value that was supplied and is out of range is an error rather
// than something NormalizeDegradationConfig would silently rewrite. Callers
// therefore validate the raw payload first and the normalized result second.
func ValidateDegradationConfig(cfg DegradationDetectionConfig) error {
	if cfg.IntervalMinute != 0 && (cfg.IntervalMinute < degradationMinIntervalMinute || cfg.IntervalMinute > degradationMaxIntervalMinute) {
		return errors.New("检测间隔必须在 1-1440 分钟之间")
	}
	if cfg.SuspendMinute != 0 && (cfg.SuspendMinute < degradationMinSuspendMinute || cfg.SuspendMinute > degradationMaxSuspendMinute) {
		return errors.New("暂停时长必须在 1-1440 分钟之间")
	}
	if cfg.PreviewIntervalMinute != 0 && (cfg.PreviewIntervalMinute < degradationMinIntervalMinute || cfg.PreviewIntervalMinute > degradationMaxIntervalMinute) {
		return errors.New("公开页刷新间隔必须在 1-1440 分钟之间")
	}
	if cfg.TimeoutSeconds != 0 && (cfg.TimeoutSeconds < 30 || cfg.TimeoutSeconds > 600) {
		return errors.New("超时时间必须在 30-600 秒之间")
	}
	if len([]rune(strings.TrimSpace(cfg.Model))) > 200 {
		return errors.New("模型 ID 不超过 200 字符")
	}
	if len([]rune(strings.TrimSpace(cfg.PreviewModel))) > 200 {
		return errors.New("公开页模型 ID 不超过 200 字符")
	}
	if strings.TrimSpace(cfg.ReasoningEffort) != "" && NormalizeMaxReasoningEffort(cfg.ReasoningEffort) == "" {
		return errors.New("思考强度必须是 minimal/low/medium/high/xhigh/max 之一")
	}
	if strings.TrimSpace(cfg.PreviewReasoningEffort) != "" && NormalizeMaxReasoningEffort(cfg.PreviewReasoningEffort) == "" {
		return errors.New("公开页思考强度必须是 minimal/low/medium/high/xhigh/max 之一")
	}
	if len([]rune(strings.TrimSpace(cfg.ExpectedAnswer))) > 200 {
		return errors.New("标准答案不超过 200 字符")
	}
	if len([]rune(cfg.Prompt)) > degradationMaxPromptRunes {
		return errors.New("提示词超过 16000 字符上限")
	}
	if err := validateIntelligentTextModel(cfg.Model); err != nil {
		return err
	}
	if err := validateIntelligentTextModel(cfg.PreviewModel); err != nil {
		return err
	}
	return nil
}

// Detector errors surfaced to the API layer.
var (
	ErrDegradationGroupNotFound = infraerrors.NotFound("DEGRADATION_GROUP_NOT_FOUND", "分组不存在")
	ErrDegradationWorkNotFound  = infraerrors.NotFound("DEGRADATION_WORK_NOT_FOUND", "作品不存在")
)

// DegradationGroup is one group's detector configuration plus its account count.
type DegradationGroup struct {
	GroupID      int64                      `json:"group_id"`
	GroupName    string                     `json:"group_name"`
	Platform     string                     `json:"platform"`
	AccountCount int64                      `json:"account_count"`
	Config       DegradationDetectionConfig `json:"config"`
}

// DegradationAccountState describes one account's detector-visible status.
type DegradationAccountState struct {
	AccountID        int64      `json:"account_id"`
	AccountName      string     `json:"account_name"`
	GroupID          int64      `json:"group_id"`
	Schedulable      bool       `json:"schedulable"`
	SuspendedUntil   *time.Time `json:"suspended_until"`
	SuspendedAt      *time.Time `json:"suspended_at"`
	SuspendNote      string     `json:"suspend_note"`
	LastProbeAt      *time.Time `json:"last_probe_at"`
	LastProbeStatus  string     `json:"last_probe_status"`
	LastProbeAnswer  string     `json:"last_probe_answer"`
	LastProbeCorrect bool       `json:"last_probe_correct"`
}

// DegradationOverview powers the admin panel summary.
type DegradationOverview struct {
	GroupsEnabled     int64                     `json:"groups_enabled"`
	AccountsWatched   int64                     `json:"accounts_watched"`
	ProbesToday       int64                     `json:"probes_today"`
	DegradedAccounts  int64                     `json:"degraded_accounts"`
	SuspendedAccounts int64                     `json:"suspended_accounts"`
	ManualDisabled    int64                     `json:"manual_disabled"`
	RecoveredToday    int64                     `json:"recovered_today"`
	PreviewEnabled    bool                      `json:"preview_enabled"`
	PreviewInterval   int                       `json:"preview_interval_minutes"`
	PreviewWorks      int64                     `json:"preview_works"`
	LastPreviewStatus string                    `json:"last_preview_status"`
	IntervalMinute    int                       `json:"interval_minutes"`
	Model             string                    `json:"model"`
	ReasoningEffort   string                    `json:"reasoning_effort"`
	ExpectedAnswer    string                    `json:"expected_answer"`
	SuspendMinute     int                       `json:"suspend_minutes"`
	Accounts          []DegradationAccountState `json:"accounts,omitempty"`
}

// DegradationPublicWork is one artwork entry on the public page. It carries no
// prompt, credential, raw output or error text.
type DegradationPublicWork struct {
	ID              int64      `json:"id"`
	AccountID       int64      `json:"account_id"`
	Status          string     `json:"status"`
	Model           string     `json:"model"`
	ReasoningEffort string     `json:"reasoning_effort"`
	HasImage        bool       `json:"has_image"`
	Image           string     `json:"image,omitempty"`
	DurationMS      int64      `json:"duration_ms"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// DegradationPublicPage is the public payload for /jiangzhijiance/.
type DegradationPublicPage struct {
	Headline        string                  `json:"headline"`
	Model           string                  `json:"model"`
	ReasoningEffort string                  `json:"reasoning_effort"`
	IntervalSeconds int                     `json:"interval_seconds"`
	Total           int64                   `json:"total"`
	Page            int                     `json:"page"`
	PageSize        int                     `json:"page_size"`
	LastStatus      string                  `json:"last_status"`
	LastFinishedAt  *time.Time              `json:"last_finished_at"`
	Items           []DegradationPublicWork `json:"items"`
}

// DegradationTimelineBucket is one time slice of probe verdicts. It is public,
// so it carries counts only: no account id, no answer, no prompt.
type DegradationTimelineBucket struct {
	Start        time.Time `json:"start"`
	Total        int64     `json:"total"`
	Correct      int64     `json:"correct"`
	Degraded     int64     `json:"degraded"`
	Undetermined int64     `json:"undetermined"`
}

// DegradationTimeline is the health chart behind the public page: it answers
// asks whether the model was degraded during the window, and it does so with
// bucket counts only, never with account rows.
type DegradationTimeline struct {
	RangeHours   int                         `json:"range_hours"`
	BucketMinute int                         `json:"bucket_minutes"`
	GeneratedAt  time.Time                   `json:"generated_at"`
	Total        int64                       `json:"total"`
	Correct      int64                       `json:"correct"`
	Degraded     int64                       `json:"degraded"`
	Undetermined int64                       `json:"undetermined"`
	HealthyRatio float64                     `json:"healthy_ratio"`
	CurrentState string                      `json:"current_state"`
	Suspended    int64                       `json:"suspended_accounts"`
	LastProbeAt  *time.Time                  `json:"last_probe_at"`
	Buckets      []DegradationTimelineBucket `json:"buckets"`
}

// DegradationWorkPage is the admin-facing artwork list. The public page is a
// curated surface, so the rows it renders can be listed and removed.
type DegradationWorkPage struct {
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Items    []DegradationPublicWork `json:"items"`
}

// DegradationRepository is implemented by the SQL repository.
type DegradationRepository interface {
	Groups(context.Context) ([]DegradationGroup, error)
	UpdateGroupConfig(context.Context, int64, int64, DegradationDetectionConfig) error
	SetTestSettingEnabled(context.Context, string, bool, bool) error

	PendingQueueDepth(context.Context) (int, error)
	DueProbeAccountIDs(context.Context, int64, int, int) ([]int64, error)
	DuePreviewAccountIDs(context.Context, []int64, int, int) ([]int64, error)
	// EnqueueDegradationTest queues one test. expectedAnswer is the group's
	// configured number, so a verdict is always resolved against the same value
	// the operator sees in the admin panel; an empty value falls back to the
	// built-in default and the artwork kind ignores it entirely.
	EnqueueDegradationTest(context.Context, int64, string, string, string, string, string, int) (int64, bool, error)
	ApplyProbeOutcome(context.Context, int64, bool, int, string) (bool, error)

	Overview(context.Context, int) (*DegradationOverview, error)
	GroupConfig(context.Context, int64) (DegradationDetectionConfig, error)
	AccountConfig(context.Context, int64) (int64, DegradationDetectionConfig, error)
	PreviewGroups(context.Context) ([]int64, error)

	PublicPage(context.Context, int, int) (*DegradationPublicPage, error)
	Timeline(context.Context, int) (*DegradationTimeline, error)
	Works(context.Context, int, int) (*DegradationWorkPage, error)
	// DeleteWork removes one artwork from the public feed. Only artwork rows are
	// ever matched, so a mistaken call cannot delete probe history.
	DeleteWork(context.Context, int64) (bool, error)
	PurgeWorks(context.Context) (int64, error)
	PublicWork(context.Context, int64) (*DegradationPublicWork, error)
}
