package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Codex turn-state pooling.
//
// ChatGPT's Codex backend issues an opaque state token to a client that it
// considers to have "paid" for this turn. The token is what the public write-up
// calls `current_turn_state`; on the wire this deployment returns it as the
// `x-codex-turn-state` RESPONSE header on a successful /backend-api/codex
// request, and it is expected back as a REQUEST header on the next call.
//
// Verified upstream behaviour (2026-09-18, live plus-group accounts):
//   - every successful Codex response carries a fresh token; a token is issued
//     from a datacenter IP as well as from a residential one;
//   - presenting a captured token is accepted: upstream answers 200 and does
//     NOT issue a replacement, i.e. the header is honoured rather than ignored;
//   - presenting a corrupted token is also accepted, but upstream then issues a
//     replacement, i.e. an unusable token degrades to "no token";
//   - the token is Fernet-shaped, so its issue time can be read back from it.
//
// Consequently the state is account-bound: a token issued to account A is never
// injected into account B's traffic. The pool is therefore keyed by account.
const (
	// SettingKeyCodexTurnStateConfig stores the single global configuration as JSON.
	SettingKeyCodexTurnStateConfig = "codex_turn_state_config"

	// CodexTurnStateDefaultModel matches the model the wider degradation tooling
	// already probes with, so the collection request is not a new shape upstream.
	CodexTurnStateDefaultModel = "gpt-6-astra"
	// CodexTurnStateDefaultHeader is the header the state travels in, both as a
	// response header and as the request header we inject.
	CodexTurnStateDefaultHeader = "x-codex-turn-state"
	// CodexTurnStateDefaultPrompt is the candy question the operator's
	// degradation standard is defined on: answering 21 means the turn was NOT
	// degraded. Asking it here makes every collection request self-verifying —
	// the answer is the authoritative verdict and the token shape (332 vs 356)
	// is the cheap proxy for it — instead of a prompt that dictates its own
	// answer ("just reply 21") and therefore proves nothing about degradation.
	//
	// A collection call spends real quota, so this is the compromise: one short
	// question. The prose must stay byte-identical to the text the A/B tooling
	// sends (tools/codex-header-probe.sh) or the two are no longer comparable.
	CodexTurnStateDefaultPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4
你只需要回复答案数字，不用输出其他解释`

	CodexTurnStateDefaultTTLSeconds          = 3600
	CodexTurnStateDefaultRenewBeforeSeconds  = 300
	CodexTurnStateDefaultPollSeconds         = 45
	CodexTurnStateDefaultProbeTimeoutSeconds = 120
	CodexTurnStateDefaultMaxPerTick          = 8
	// ⚠️ Correction (2026-09-18, measured): the write-up's "292" and "312" are
	// NOT HTTP statuses — they are the CHARACTER COUNTS of the turn-state token
	// itself. They are the individual-account shapes (normal 292 / degraded 312);
	// team accounts, which is what this deployment's pool is made of, use
	// 332 / 356. Both constants are kept only because the probe still tolerates
	// a state carried in the body of a non-standard status; no such status has
	// ever been observed here, and the revocation default is now empty on
	// purpose (a length is not a status to revoke on).
	CodexTurnStateDefaultRevocationStatus = 312
	CodexTurnStateDefaultStateStatus      = 292

	codexTurnStateMinTTLSeconds          = 60
	codexTurnStateMaxTTLSeconds          = 86400
	codexTurnStateMinPollSeconds         = 5
	codexTurnStateMaxPollSeconds         = 3600
	codexTurnStateMinProbeTimeoutSeconds = 10
	codexTurnStateMaxProbeTimeoutSeconds = 600
	codexTurnStateMaxAccountsPerTick     = 200
	codexTurnStateMaxEnrolledAccounts    = 20000
	codexTurnStateMaxPromptRunes         = 4000
	codexTurnStateMaxGroupIDs            = 200
	codexTurnStateMaxInjectModels        = 20
	codexTurnStateMaxModelEntryRunes     = 96
	// codexTurnStateModelProbeBytes bounds the body replay used to read the
	// requested model. Codex bodies start with the model, so this is generous.
	codexTurnStateModelProbeBytes       = 256 << 10
	codexTurnStateHistoryKeepPerAccount = 50
	codexTurnStateHistoryRetention      = 7 * 24 * time.Hour
	codexTurnStatePruneInterval         = time.Hour
	codexTurnStateConfigCacheTTL        = 15 * time.Second
	codexTurnStateEventQueueSize        = 512

	// State record statuses.
	CodexTurnStateStatusActive   = "active"
	CodexTurnStateStatusRevoked  = "revoked"
	CodexTurnStateStatusFailed   = "failed"
	CodexTurnStateStatusExpired  = "expired"
	CodexTurnStateStatusDegraded = "degraded"

	// State record sources.
	CodexTurnStateSourceHarvest = "harvest"
	CodexTurnStateSourceProbe   = "probe"
	CodexTurnStateSourceManual  = "manual"

	// Token shapes. An account class always uses the same pair of shapes, and the
	// degraded variant is exactly one cipher block longer than the normal one.
	CodexTurnStateShapeIndividual = "individual"
	CodexTurnStateShapeTeam       = "team"
	CodexTurnStateShapeUnknown    = "unknown"

	// Injection modes. FillEmpty preserves the caller's own turn state — the
	// upstream issues one to every Codex client, so this only ever helps a caller
	// that sent none. Force overwrites it, which is the only mode that can move a
	// downgraded turn back onto the good route: measured 2026-09-18, replaying a
	// normal 332 state answered correctly 12/12 times across 12 different exit
	// IPs, against a ~9% cold hit rate on the same pool.
	CodexTurnStateInjectModeFillEmpty = "fill_empty"
	CodexTurnStateInjectModeForce     = "force"
)

// Errors surfaced to the API layer.
var (
	ErrCodexTurnStateDisabled    = errors.New("292 状态注入未启用")
	ErrCodexTurnStateAccountMiss = errors.New("账号不存在")
	ErrCodexTurnStateNoAccounts  = errors.New("没有可用的采集账号，请检查检测分组与指定账号配置")
)

// ---------------------------------------------------------------- token shape
//
// The ciphertext size is the only part of a state token that can be compared
// without the server key, and it turns out to be the whole story: measured
// 2026-09-18 on the east-us pool, 47 requests / 60 samples,
//
//	team  normal   12 blocks / 332 chars  -> served model gpt-6-astra, candy answer 21
//	team  degraded 13 blocks / 356 chars  -> served model gpt-5.6-luna, candy answer 28/29/36
//	personal       normal 10 / 292, degraded 11 / 312 (the write-up's two numbers)
//
// with zero exceptions in either direction. Downgrading a turn adds exactly one
// cipher block to whichever baseline applies, so the check has to be per-shape:
// a single "more than N blocks" threshold reads every team token as degraded.
// The same table was arrived at independently by the gpt-load fork
// (commit a4f9f360 "treat 332-char team turn states as normal").
const (
	codexTurnStateFernetOverhead = 57 // 1 version + 8 timestamp + 16 IV + 32 HMAC
)

var codexTurnStateShapeTable = map[string]struct {
	NormalBlocks   int
	DegradedBlocks int
	NormalChars    int
	DegradedChars  int
}{
	CodexTurnStateShapeIndividual: {NormalBlocks: 10, DegradedBlocks: 11, NormalChars: 292, DegradedChars: 312},
	CodexTurnStateShapeTeam:       {NormalBlocks: 12, DegradedBlocks: 13, NormalChars: 332, DegradedChars: 356},
}

// CodexTurnStateInfo is everything that can be told about a token without the
// key that produced it.
type CodexTurnStateInfo struct {
	Shape  string
	Blocks int
	Chars  int
	// Normal is true only when the block count hits a normal shape. A token that
	// does not parse as Fernet is Unknown, not degraded: an unreadable value is
	// not evidence that this turn was downgraded.
	Normal bool
}

// ClassifyCodexTurnState reads the Fernet envelope of a token. The layout is
// 0x80 | 8-byte timestamp | 16-byte IV | AES-CBC ciphertext | 32-byte HMAC, so
// the ciphertext size falls straight out of the decoded length.
func ClassifyCodexTurnState(token string) CodexTurnStateInfo {
	token = strings.TrimSpace(token)
	info := CodexTurnStateInfo{Shape: CodexTurnStateShapeUnknown, Chars: len(token)}
	if token == "" {
		return info
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
	if err != nil || len(raw) < codexTurnStateFernetOverhead+16 || raw[0] != 0x80 {
		return info
	}
	size := len(raw) - codexTurnStateFernetOverhead
	if size%16 != 0 {
		return info
	}
	info.Blocks = size / 16
	switch info.Blocks {
	case codexTurnStateShapeTable[CodexTurnStateShapeIndividual].NormalBlocks:
		info.Shape, info.Normal = CodexTurnStateShapeIndividual, true
	case codexTurnStateShapeTable[CodexTurnStateShapeTeam].NormalBlocks:
		info.Shape, info.Normal = CodexTurnStateShapeTeam, true
	case codexTurnStateShapeTable[CodexTurnStateShapeIndividual].DegradedBlocks:
		info.Shape = CodexTurnStateShapeIndividual
	case codexTurnStateShapeTable[CodexTurnStateShapeTeam].DegradedBlocks:
		info.Shape = CodexTurnStateShapeTeam
	}
	return info
}

// CodexTurnStateShapeSummary renders the table for API payloads and log lines.
func CodexTurnStateShapeSummary(info CodexTurnStateInfo) string {
	switch {
	case info.Shape == CodexTurnStateShapeUnknown:
		return fmt.Sprintf("未知形态(%d 字符 / %d 块)", info.Chars, info.Blocks)
	case info.Normal:
		return fmt.Sprintf("%s 正常形态(%d 字符 / %d 块)", info.Shape, info.Chars, info.Blocks)
	default:
		return fmt.Sprintf("%s 疑似降智(%d 字符 / %d 块)", info.Shape, info.Chars, info.Blocks)
	}
}

// CodexTurnStateConfig is the single global configuration. Zero values are
// filled by NormalizeCodexTurnStateConfig.
type CodexTurnStateConfig struct {
	Enabled                 bool   `json:"enabled"`
	TransferEnabled         bool   `json:"transfer_enabled"`
	TransferReadyGroupID    int64  `json:"transfer_ready_group_id"`
	TransferRecoveryGroupID int64  `json:"transfer_recovery_group_id"`
	CycleCooldownSeconds    int    `json:"cycle_cooldown_seconds"`
	HarvestTransport        string `json:"harvest_transport"`
	// ProxyID points at a row in the proxies table, so the dynamic-IP proxy is
	// managed (and testable) through the existing 代理管理 page rather than
	// duplicated here. 0 means "use each account's own proxy".
	ProxyID int64 `json:"proxy_id"`
	// GroupIDs enrolls every account of those groups; AccountIDs adds individual
	// accounts on top. Both are optional but at least one is required to collect.
	GroupIDs   []int64 `json:"group_ids"`
	AccountIDs []int64 `json:"account_ids"`

	Model  string `json:"model"`
	Prompt string `json:"prompt"`

	TTLSeconds          int `json:"ttl_seconds"`
	RenewBeforeSeconds  int `json:"renew_before_seconds"`
	PollSeconds         int `json:"poll_seconds"`
	ProbeTimeoutSeconds int `json:"probe_timeout_seconds"`
	MaxAccountsPerTick  int `json:"max_accounts_per_tick"`
	// Zero keeps the shape-based policy. A positive length is an explicit
	// operator-selected target and overrides the shape policy, not the TTL.
	TargetStateLength   int `json:"target_state_length"`
	MaxAttemptsPerCycle int `json:"max_attempts_per_cycle"`
	ConcurrentAccounts  int `json:"concurrent_accounts"`
	RetryIntervalMS     int `json:"retry_interval_ms"`

	InjectEnabled bool   `json:"inject_enabled"`
	InjectHeader  string `json:"inject_header"`
	// InjectMode decides what happens when the caller already presents a state.
	// See CodexTurnStateInjectModeForce: only "force" can rescue a turn the
	// upstream already downgraded, because a real Codex client always sends its
	// own (degraded) token and "fill_empty" would leave it in place.
	InjectMode string `json:"inject_mode"`
	// AllowDegradedShapes keeps tokens whose block count sits one above their
	// shape baseline. Those are the tokens the upstream hands out when it
	// downgrades the turn, so injecting one pins the downgrade — they are
	// recorded either way, but the default refuses to make them injectable.
	AllowDegradedShapes bool `json:"allow_degraded_shapes"`
	// InjectModels narrows injection to these requested models. An entry ending in
	// * matches by prefix; a single * means every model; an unreadable model is
	// injected anyway, because this list exists to narrow injection, never to
	// swallow it silently.
	//
	// Leaving it empty means "the model this pool was collected with" (Model),
	// which is the safe reading of the measurement: upstream REFUSES a state on a
	// request for another model and replaces it with a fresh one — replaying a
	// 332 collected under gpt-6-astra on gpt-5.5 and gpt-5.6-sol requests was
	// rejected 6/6 times, while the same state was accepted 15/15 times on
	// gpt-6-astra. Injecting across models therefore only throws away the state
	// the caller already had.
	InjectModels []string `json:"inject_models"`

	RevocationStatuses []int `json:"revocation_statuses"`
	// IssuanceStatuses restricts which upstream statuses may introduce a state.
	// Empty means "any response that carries one" — the observed behaviour on
	// this upstream, where a normal 200 carries x-codex-turn-state.
	//
	// Set it to [292] to trust ONLY the write-up's pass-issuance status. That is
	// the strict reading: in an environment where 292 appears, it keeps a
	// 200-issued token out of the pool. In an environment where 292 never
	// appears it leaves the pool empty on purpose, so it must be chosen
	// deliberately rather than by default.
	IssuanceStatuses []int `json:"issuance_statuses"`
}

// NormalizeCodexTurnStateConfig fills defaults and canonicalizes valid values.
func NormalizeCodexTurnStateConfig(cfg CodexTurnStateConfig) CodexTurnStateConfig {
	if cfg.CycleCooldownSeconds == 0 {
		cfg.CycleCooldownSeconds = 300
	}
	if cfg.HarvestTransport == "" {
		cfg.HarvestTransport = "account"
	}
	// Retained in JSON for old clients, but never permits non-target states.
	cfg.AllowDegradedShapes = false
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = CodexTurnStateDefaultModel
	}
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	if cfg.Prompt == "" {
		cfg.Prompt = CodexTurnStateDefaultPrompt
	}
	cfg.InjectHeader = http.CanonicalHeaderKey(strings.TrimSpace(cfg.InjectHeader))
	if cfg.InjectHeader == "" {
		cfg.InjectHeader = http.CanonicalHeaderKey(CodexTurnStateDefaultHeader)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.InjectMode)) {
	case CodexTurnStateInjectModeForce:
		cfg.InjectMode = CodexTurnStateInjectModeForce
	default:
		cfg.InjectMode = CodexTurnStateInjectModeFillEmpty
	}
	if cfg.TTLSeconds == 0 {
		cfg.TTLSeconds = CodexTurnStateDefaultTTLSeconds
	}
	if cfg.RenewBeforeSeconds == 0 {
		cfg.RenewBeforeSeconds = CodexTurnStateDefaultRenewBeforeSeconds
	}
	if cfg.PollSeconds == 0 {
		cfg.PollSeconds = CodexTurnStateDefaultPollSeconds
	}
	if cfg.ProbeTimeoutSeconds == 0 {
		cfg.ProbeTimeoutSeconds = CodexTurnStateDefaultProbeTimeoutSeconds
	}
	if cfg.MaxAccountsPerTick == 0 {
		cfg.MaxAccountsPerTick = CodexTurnStateDefaultMaxPerTick
	}
	if cfg.MaxAttemptsPerCycle == 0 {
		cfg.MaxAttemptsPerCycle = 50
	}
	if cfg.ConcurrentAccounts == 0 {
		cfg.ConcurrentAccounts = 16
	}
	if cfg.RetryIntervalMS == 0 {
		cfg.RetryIntervalMS = 200
	}
	// No default revocation status: the write-up's "312" turned out to be a token
	// length, not an HTTP status, so nothing is revoked unless an operator names
	// a status that this upstream actually returns.
	cfg.InjectModels = normalizeCodexTurnStateModels(cfg.InjectModels)
	if len(cfg.InjectModels) == 0 {
		// Scoped to the collection model by default; "*" is the explicit opt-in
		// for every model.
		cfg.InjectModels = []string{strings.ToLower(cfg.Model)}
	}
	cfg.GroupIDs = normalizeCodexTurnStateIDs(cfg.GroupIDs)
	cfg.AccountIDs = normalizeCodexTurnStateIDs(cfg.AccountIDs)
	cfg.RevocationStatuses = normalizeStatusCodes(cfg.RevocationStatuses)
	cfg.IssuanceStatuses = normalizeStatusCodes(cfg.IssuanceStatuses)
	return cfg
}

// ValidateCodexTurnStateConfig rejects operator input that is present but
// invalid. A zero value means "leave it to the defaults" and is accepted.
func ValidateCodexTurnStateConfig(cfg CodexTurnStateConfig) error {
	if cfg.HarvestTransport != "" && cfg.HarvestTransport != "account" && cfg.HarvestTransport != "independent" {
		return fmt.Errorf("采集通道必须为 account 或 independent")
	}
	if cfg.CycleCooldownSeconds != 0 && (cfg.CycleCooldownSeconds < 1 || cfg.CycleCooldownSeconds > 86400) {
		return fmt.Errorf("轮次冷却需为 1–86400 秒")
	}
	if cfg.TransferEnabled && (cfg.TransferReadyGroupID <= 0 || cfg.TransferRecoveryGroupID <= 0 || cfg.TransferReadyGroupID == cfg.TransferRecoveryGroupID) {
		return fmt.Errorf("请选择不同的可用分组 A 和待恢复分组 B")
	}
	if cfg.Enabled && cfg.HarvestTransport == "independent" && cfg.ProxyID <= 0 {
		return fmt.Errorf("独立采集通道必须配置有效的采集代理")
	}
	if cfg.TransferActive() && !cfg.InjectionMatchesModel(cfg.Model) {
		return fmt.Errorf("双向移组要求注入模型规则包含当前采集模型")
	}
	if cfg.ConcurrentAccounts < 0 || cfg.ConcurrentAccounts > 64 {
		return errors.New("并发采集账号数必须在 1-64 之间")
	}
	if cfg.RetryIntervalMS != 0 && (cfg.RetryIntervalMS < 50 || cfg.RetryIntervalMS > 60000) {
		return errors.New("连续采集间隔必须在 50-60000 毫秒之间")
	}
	if cfg.TargetStateLength != 0 && cfg.TargetStateLength != 292 && cfg.TargetStateLength != 332 {
		return errors.New("目标状态长度只能是 292（个人）、332（Team）或 0（自动识别）")
	}
	if cfg.MaxAttemptsPerCycle < 0 || cfg.MaxAttemptsPerCycle > 10000 {
		return errors.New("每账号每轮连续采集上限必须在 1-10000 之间")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.InjectMode)) {
	case "", CodexTurnStateInjectModeFillEmpty, CodexTurnStateInjectModeForce:
	default:
		return errors.New("注入模式只能是 fill_empty（只补空白头）或 force（覆盖客户端回带的头）")
	}
	if cfg.TTLSeconds != 0 && (cfg.TTLSeconds < codexTurnStateMinTTLSeconds || cfg.TTLSeconds > codexTurnStateMaxTTLSeconds) {
		return errors.New("状态有效期必须在 60-86400 秒之间")
	}
	if cfg.RenewBeforeSeconds < 0 || cfg.RenewBeforeSeconds > codexTurnStateMaxTTLSeconds {
		return errors.New("提前续期时间必须在 0-86400 秒之间")
	}
	if cfg.RenewBeforeSeconds > 0 && cfg.TTLSeconds > 0 && cfg.RenewBeforeSeconds >= cfg.TTLSeconds {
		return errors.New("提前续期时间必须小于状态有效期")
	}
	if cfg.PollSeconds != 0 && (cfg.PollSeconds < codexTurnStateMinPollSeconds || cfg.PollSeconds > codexTurnStateMaxPollSeconds) {
		return errors.New("轮询间隔必须在 5-3600 秒之间")
	}
	if cfg.ProbeTimeoutSeconds != 0 && (cfg.ProbeTimeoutSeconds < codexTurnStateMinProbeTimeoutSeconds || cfg.ProbeTimeoutSeconds > codexTurnStateMaxProbeTimeoutSeconds) {
		return errors.New("采集超时必须在 10-600 秒之间")
	}
	if cfg.MaxAccountsPerTick != 0 && (cfg.MaxAccountsPerTick < 1 || cfg.MaxAccountsPerTick > codexTurnStateMaxAccountsPerTick) {
		return errors.New("每轮最多采集账号数必须在 1-200 之间")
	}
	if len([]rune(strings.TrimSpace(cfg.Model))) > 200 {
		return errors.New("模型 ID 不超过 200 字符")
	}
	if cfg.Model != "" {
		if err := validateIntelligentTextModel(cfg.Model); err != nil {
			return err
		}
	}
	if len([]rune(cfg.Prompt)) > codexTurnStateMaxPromptRunes {
		return errors.New("提示词超过 4000 字符上限")
	}
	if len(cfg.GroupIDs) > codexTurnStateMaxGroupIDs {
		return errors.New("检测分组数量过多")
	}
	if len(cfg.AccountIDs) > codexTurnStateMaxEnrolledAccounts {
		return errors.New("指定账号数量过多")
	}
	if cfg.InjectHeader != "" && !isValidHeaderToken(cfg.InjectHeader) {
		return errors.New("注入请求头名称不合法")
	}
	if len(cfg.InjectModels) > codexTurnStateMaxInjectModels {
		return errors.New("注入模型名单最多 20 条")
	}
	for _, entry := range cfg.InjectModels {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if len([]rune(entry)) > codexTurnStateMaxModelEntryRunes {
			return errors.New("注入模型名单里有超长条目")
		}
		if !validCodexTurnStateModelEntry(entry) {
			return errors.New("注入模型名单只允许字母数字与 - . _ / ，结尾可用 * 做前缀匹配")
		}
	}
	for _, code := range cfg.RevocationStatuses {
		if code < 100 || code > 599 {
			return errors.New("撤销状态码必须是 100-599 之间的整数")
		}
	}
	for _, code := range cfg.IssuanceStatuses {
		if code < 100 || code > 599 {
			return errors.New("下发状态码必须是 100-599 之间的整数")
		}
	}
	if cfg.ProxyID < 0 {
		return errors.New("动态IP代理选择不合法")
	}
	if cfg.Enabled {
		if len(normalizeInt64IDs(cfg.GroupIDs)) == 0 && len(normalizeInt64IDs(cfg.AccountIDs)) == 0 && !cfg.TransferActive() {
			return ErrCodexTurnStateNoAccounts
		}
	}
	return nil
}

func (cfg CodexTurnStateConfig) AcceptsStateToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	info := ClassifyCodexTurnState(token)
	if !info.Normal || (len(token) != 292 && len(token) != 332) {
		return false
	}
	return cfg.TargetStateLength == 0 || len(token) == cfg.TargetStateLength
}

func normalizeCodexTurnStateIDs(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeCodexTurnStateModels canonicalizes the injection allow-list: trimmed,
// lower-cased, de-duplicated, order preserved.
func normalizeCodexTurnStateModels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validCodexTurnStateModelEntry accepts what model names are actually made of,
// plus the trailing * that turns an entry into a prefix match.
func validCodexTurnStateModelEntry(entry string) bool {
	entry = strings.TrimSuffix(entry, "*")
	if entry == "" {
		return false
	}
	for _, r := range entry {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_' || r == '/':
		default:
			return false
		}
	}
	return true
}

// InjectionMatchesModel reports whether a request for this model may receive the
// pooled state. An empty list means every model; an unreadable model ("" — the
// body was not replayable, or the caller sent none) is injected, because the
// list is meant to narrow injection, never to swallow it silently.
func (cfg CodexTurnStateConfig) InjectionMatchesModel(model string) bool {
	if len(cfg.InjectModels) == 0 {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return true
	}
	for _, entry := range cfg.InjectModels {
		if prefix, ok := strings.CutSuffix(entry, "*"); ok {
			if strings.HasPrefix(model, prefix) {
				return true
			}
			continue
		}
		if entry == model {
			return true
		}
	}
	return false
}

func normalizeStatusCodes(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, v := range values {
		if v < 100 || v > 599 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isValidHeaderToken(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// AcceptsIssuanceStatus reports whether a response carrying a state is allowed to
// introduce one. An empty list accepts any status: that is the observed
// behaviour here, where a normal 200 carries the state header.
func (cfg CodexTurnStateConfig) AcceptsIssuanceStatus(statusCode int) bool {
	if len(cfg.IssuanceStatuses) == 0 {
		return statusCode >= 200 && statusCode < 300
	}
	return isStatusIn(statusCode, cfg.IssuanceStatuses)
}

// AcceptsRevocationStatus reports whether a status revokes the current state.
func (cfg CodexTurnStateConfig) AcceptsRevocationStatus(statusCode int) bool {
	return isStatusIn(statusCode, cfg.RevocationStatuses)
}

// CodexTurnStateStatuses returns the statuses a record may carry.
func IsCodexTurnStateStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case CodexTurnStateStatusActive, CodexTurnStateStatusRevoked, CodexTurnStateStatusFailed, CodexTurnStateStatusExpired, CodexTurnStateStatusDegraded:
		return true
	default:
		return false
	}
}

// CodexTurnStateRecord is one observation of an account's state.
//
// State carries the raw token because injection needs it, and it is excluded
// from JSON so no admin response can ever leak it.
type CodexTurnStateRecord struct {
	ID               int64      `json:"id"`
	AccountID        int64      `json:"account_id"`
	AccountName      string     `json:"account_name"`
	State            string     `json:"-"`
	Status           string     `json:"status"`
	Source           string     `json:"source"`
	HTTPStatus       int        `json:"http_status"`
	Model            string     `json:"model"`
	ProxyID          int64      `json:"proxy_id"`
	LatencyMS        int64      `json:"latency_ms"`
	StateFingerprint string     `json:"state_fingerprint"`
	StateLength      int        `json:"state_length"`
	IssuedAt         *time.Time `json:"issued_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	Error            string     `json:"error"`
	CreatedAt        time.Time  `json:"created_at"`
}

// CodexTurnStateAccountRow is the per-account view for the admin page. It never
// carries the raw token: only a fingerprint and the remaining time.
type CodexTurnStateAccountRow struct {
	TicketQualified      bool       `json:"ticket_qualified"`
	TransferStatus       string     `json:"transfer_status"`
	TransferError        string     `json:"transfer_error"`
	NextRetryAt          *time.Time `json:"next_retry_at"`
	CurrentGroupIDs      []int64    `json:"current_group_ids"`
	AccountID            int64      `json:"account_id"`
	AccountName          string     `json:"account_name"`
	GroupID              int64      `json:"group_id"`
	HasState             bool       `json:"has_state"`
	StateStatus          string     `json:"state_status"`
	StateFingerprint     string     `json:"state_fingerprint"`
	StateLength          int        `json:"state_length"`
	IssuedAt             *time.Time `json:"issued_at"`
	ExpiresAt            *time.Time `json:"expires_at"`
	RemainingSeconds     int64      `json:"remaining_seconds"`
	Source               string     `json:"source"`
	HTTPStatus           int        `json:"http_status"`
	LatencyMS            int64      `json:"latency_ms"`
	RecordedAt           *time.Time `json:"recorded_at"`
	Error                string     `json:"error"`
	ProbeAttempts        int        `json:"probe_attempts"`
	AttemptsLimitReached bool       `json:"attempts_limit_reached"`
	CollectionStatus     string     `json:"collection_status"`
}

// CodexTurnStateOverview powers the admin panel.
type CodexTurnStateOverview struct {
	Transfers []CodexTurnStateTransfer `json:"transfers"`
	CodexTurnStateConfig
	ProxyName          string                     `json:"proxy_name"`
	ProxyConfigured    bool                       `json:"proxy_configured"`
	EnrolledAccounts   int                        `json:"enrolled_accounts"`
	ActiveStates       int                        `json:"active_states"`
	ExpiringSoon       int                        `json:"expiring_soon"`
	RevokedStates      int                        `json:"revoked_states"`
	FailedStates       int                        `json:"failed_states"`
	ProbesToday        int64                      `json:"probes_today"`
	HarvestsToday      int64                      `json:"harvests_today"`
	LastProbeAt        *time.Time                 `json:"last_probe_at"`
	LastProbeStatus    string                     `json:"last_probe_status"`
	LastError          string                     `json:"last_error"`
	NextTickAt         *time.Time                 `json:"next_tick_at"`
	Accounts           []CodexTurnStateAccountRow `json:"accounts"`
	CollectingAccounts int                        `json:"collecting_accounts"`
	QueuedAccounts     int                        `json:"queued_accounts"`
}

// CodexTurnStateHistoryPage is the paginated observation log.
type CodexTurnStateHistoryPage struct {
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
	Items    []CodexTurnStateRecord `json:"items"`
}

// CodexTurnStateRepository is implemented by the SQL repository.
type CodexTurnStateRepository interface {
	Config(ctx context.Context) (CodexTurnStateConfig, error)
	SaveConfig(ctx context.Context, actor int64, cfg CodexTurnStateConfig) error

	// LatestPerAccount returns the newest observation for each requested account.
	LatestPerAccount(ctx context.Context, accountIDs []int64) (map[int64]CodexTurnStateRecord, error)
	Insert(ctx context.Context, record *CodexTurnStateRecord) (int64, error)
	// Touch refreshes the TTL of an already-known state without adding a row.
	Touch(ctx context.Context, id int64, expiresAt time.Time) error
	Invalidate(ctx context.Context, accountID int64, note string) error
	ExpireStale(ctx context.Context, now time.Time) (int64, error)
	History(ctx context.Context, accountID int64, page, pageSize int) (*CodexTurnStateHistoryPage, error)
	CountersSince(ctx context.Context, since time.Time) (probes int64, harvests int64, err error)
	Prune(ctx context.Context, keepPerAccount int, olderThan time.Time) (int64, error)

	// EnrolledAccounts resolves the configured groups to the accounts that can
	// actually serve traffic, returning accountID -> groupID.
	EnrolledAccounts(ctx context.Context, groupIDs []int64) (map[int64]int64, error)
}

// decodeFernetIssuedAt reads the issue timestamp out of a Fernet-shaped token.
//
// The token is opaque, so this is strictly best effort: it returns the zero time
// when the shape is not recognised, and callers fall back to the observation
// time. Only the version byte and the following 8-byte big-endian timestamp are
// read, which is the public part of the format.
func decodeFernetIssuedAt(token string) time.Time {
	token = strings.TrimSpace(token)
	if len(token) < 16 {
		return time.Time{}
	}
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
		if err != nil {
			return time.Time{}
		}
	}
	if len(raw) < 9 || raw[0] != 0x80 {
		return time.Time{}
	}
	var seconds uint64
	for i := 1; i <= 8; i++ {
		seconds = seconds<<8 | uint64(raw[i])
	}
	if seconds == 0 {
		return time.Time{}
	}
	issued := time.Unix(int64(seconds), 0).UTC()
	// Reject absurd values rather than showing a nonsense countdown: anything
	// outside a sane window around now cannot be a real issue time.
	now := time.Now().UTC()
	if issued.After(now.Add(24*time.Hour)) || issued.Before(now.Add(-30*24*time.Hour)) {
		return time.Time{}
	}
	return issued
}

// stateFingerprint is the non-reversible short form shown to operators.
func stateFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}
