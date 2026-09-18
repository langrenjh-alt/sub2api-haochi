// Package wishteam implements a durable, opt-in WishTeam5X account repair worker.
package wishteam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const Endpoint = "https://team5x.wishtoapp.com"

var ErrBusy = errors.New("已有巡查任务在执行，请等待本轮完成")

type Config struct {
	Enabled         bool      `json:"enabled"`
	GroupID         int64     `json:"group_id"`
	IntervalMinutes int       `json:"interval_minutes"`
	NextRunAt       time.Time `json:"next_run_at"`
}

type Group struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Run struct {
	ID         int64      `json:"id"`
	GroupID    int64      `json:"group_id"`
	Status     string     `json:"status"`
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Alive      int        `json:"alive"`
	Replaced   int        `json:"replaced"`
	Revived    int        `json:"revived"`
	Failed     int        `json:"failed"`
	Skipped    int        `json:"skipped"`
	Dead       int        `json:"workspace_dead"`
	Message    string     `json:"message"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Probe deliberately excludes tokens, arbitrary provider metadata and task IDs.
type Probe struct {
	HTTPStatus         int    `json:"http_status,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ProviderHTTPStatus int    `json:"provider_http_status,omitempty"`
	PlanType           string `json:"plan_type,omitempty"`
	State              string `json:"state,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type Item struct {
	ID           int64     `json:"id"`
	AccountID    int64     `json:"account_id"`
	NewAccountID *int64    `json:"new_account_id"`
	Email        string    `json:"email"`
	Status       string    `json:"status"`
	Stage        string    `json:"stage"`
	Message      string    `json:"message"`
	ErrorCode    string    `json:"error_code"`
	Probe        Probe     `json:"probe"`
	RetryAfter   int       `json:"retry_after"`
	Archived     bool      `json:"archived"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Document struct {
	Type       string           `json:"type"`
	Version    int              `json:"version"`
	ExportedAt string           `json:"exported_at"`
	Proxies    []any            `json:"proxies"`
	Accounts   []map[string]any `json:"accounts"`
}

type RemoteItem struct {
	Email            string `json:"email"`
	Status           string `json:"status"`
	Stage            string `json:"stage"`
	Reason           string `json:"reason"`
	ErrorCode        string `json:"error_code"`
	ReusedCurrent    bool   `json:"reused_current"`
	Probe            Probe  `json:"probe"`
	ReplacementProbe Probe  `json:"replacement_probe,omitempty"`
	RetryAfter       int    `json:"retry_after"`
}

type RemoteReply struct {
	Success    bool         `json:"success"`
	OutputMode string       `json:"output_mode,omitempty"`
	Partial    bool         `json:"partial,omitempty"`
	TaskID     string       `json:"task_id"`
	RetryAfter int          `json:"retry_after"`
	Results    []RemoteItem `json:"results"`
	Sub2       Document     `json:"sub2"`
	Task       struct {
		Status string       `json:"status"`
		Items  []RemoteItem `json:"items"`
	} `json:"task"`
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func text(v any) string { s, _ := v.(string); return s }

func emailOf(a map[string]any) string {
	for _, s := range []string{text(object(a["extra"])["email"]), text(object(a["credentials"])["email"]), text(a["email"]), text(a["name"])} {
		s = strings.ToLower(strings.TrimSpace(s))
		addr, err := mail.ParseAddress(s)
		if err == nil && addr.Address == s && strings.Contains(s, "@") {
			return s
		}
	}
	return ""
}

// Only authentication/identity fields can come from the provider. Unknown and
// future configuration keys, model whitelists, WS and fingerprint switches stay local.
var authFields = []string{
	"access_token", "refresh_token", "id_token", "token_type", "expires_at",
	"expires_in", "scope", "email", "chatgpt_account_id", "chatgpt_user_id",
	"organization_id", "account_id", "user_id", "plan_type",
}

func outbound(a map[string]any) map[string]any {
	credentials := map[string]any{}
	for _, k := range authFields {
		if v, ok := object(a["credentials"])[k]; ok {
			credentials[k] = v
		}
	}
	return map[string]any{
		"name": emailOf(a), "platform": "openai", "type": "oauth",
		"credentials": credentials, "extra": map[string]any{"email": emailOf(a)},
	}
}

func document(accounts []map[string]any) Document {
	return Document{"sub2api-data", 1, time.Now().UTC().Format(time.RFC3339), []any{}, accounts}
}

func badEvidence(item RemoteItem) bool {
	return item.Probe.HTTPStatus == 401 || strings.EqualFold(item.Probe.PlanType, "free") ||
		item.Reason == "unauthorized" || item.Reason == "free_plan" ||
		item.Probe.Reason == "unauthorized" || item.Probe.Reason == "free_plan"
}

func replacement(old, fresh map[string]any, verdict RemoteItem) (map[string]any, error) {
	if verdict.Status != "revived" && !(verdict.Status == "alive" && verdict.ReusedCurrent) {
		return nil, errors.New("该结果不需要替换账号")
	}
	// Historical tasks can omit probe details. The updated API explicitly
	// identifies a verified server-side replacement with reused_current.
	// Do not treat replacement_probe (the NEW token) as original-token evidence.
	historicalReplacement := verdict.Status == "alive" && verdict.ReusedCurrent &&
		verdict.Probe == (Probe{}) && verdict.Reason == ""
	if !badEvidence(verdict) && !historicalReplacement {
		return nil, errors.New("缺少原账号明确 401 / Free 证据，保留旧号")
	}
	if emailOf(old) == "" || emailOf(old) != emailOf(fresh) || emailOf(old) != strings.ToLower(strings.TrimSpace(verdict.Email)) {
		return nil, errors.New("返回邮箱与原账号不匹配，保留旧号")
	}
	if text(fresh["platform"]) != "openai" || text(fresh["type"]) != "oauth" {
		return nil, errors.New("返回成品不是 OpenAI OAuth 账号，保留旧号")
	}
	oldCred, newCred := object(old["credentials"]), object(fresh["credentials"])
	if text(newCred["access_token"]) == "" {
		return nil, errors.New("返回成品缺少 access_token，保留旧号")
	}
	for _, k := range []string{"chatgpt_account_id", "organization_id"} {
		if a, b := text(oldCred[k]), text(newCred[k]); a != "" && (b == "" || a != b) {
			return nil, fmt.Errorf("返回成品 %s 与原工作区不匹配，保留旧号", k)
		}
	}
	// JSON roundtrip produces a deep copy and preserves every DB column.
	raw, _ := json.Marshal(old)
	var merged map[string]any
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, err
	}
	credentials := object(merged["credentials"])
	for _, k := range authFields {
		if v, ok := newCred[k]; ok {
			credentials[k] = v
		}
	}
	merged["credentials"] = credentials
	extra := object(merged["extra"])
	// These are observed identity/plan metadata, not account settings.
	for _, k := range []string{"email", "plan_type", "subscription_type"} {
		if v, ok := object(fresh["extra"])[k]; ok {
			extra[k] = v
		}
	}
	merged["extra"] = extra
	// SetError disables BOTH status and schedulable (including on OAuth 401).
	// Repair both halves after a verified credential replacement. Treating the
	// error-induced scheduling stop as configuration leaves revived accounts idle.
	// Explicit inactive/disabled status or an active account's manual scheduling
	// stop remains unchanged, as do unrelated cooldowns and degradation isolation.
	if text(old["status"]) == "error" {
		merged["status"] = "active"
		merged["error_message"] = nil
		merged["schedulable"] = true
	}
	return merged, nil
}

func safeProbe(p Probe) Probe {
	if p.Provider != "chixiaotao" {
		p.Provider = ""
	}
	if p.ProviderHTTPStatus < 100 || p.ProviderHTTPStatus > 599 {
		p.ProviderHTTPStatus = 0
	}
	plans := map[string]bool{"free": true, "team": true, "plus": true, "pro": true, "business": true, "enterprise": true, "edu": true, "go": true, "unknown": true}
	states := map[string]bool{"alive": true, "valid": true, "unauthorized": true, "free": true, "failed": true, "unavailable": true, "unknown": true, "error": true}
	reasons := map[string]bool{"unauthorized": true, "free_plan": true, "plan_mismatch": true, "workspace_mismatch": true, "probe_unavailable": true, "usage_incomplete": true, "identity_mismatch": true}
	if !plans[strings.ToLower(p.PlanType)] {
		p.PlanType = ""
	}
	if !states[p.State] {
		p.State = ""
	}
	if !reasons[p.Reason] {
		p.Reason = ""
	}
	if p.HTTPStatus < 100 || p.HTTPStatus > 599 {
		p.HTTPStatus = 0
	}
	return p
}

func verdictMessage(r RemoteItem) string {
	switch {
	case r.ErrorCode == "workspace_deactivated":
		return "工作区已停用（炸车），等待管理员恢复；旧号保留"
	case r.Status == "not_ours":
		return "服务端无此子号产出记录，旧号保留"
	case r.RetryAfter > 0:
		return fmt.Sprintf("上游要求 %d 秒后重试，旧号保留", r.RetryAfter)
	case r.Status == "alive" && !r.ReusedCurrent:
		return "当前文件已通过检测，未更换凭据"
	case r.Status == "revived":
		return "复活成功；旧号已存档删除，新号配置已逐字段校验"
	case r.Status == "alive" && r.ReusedCurrent:
		return "已替换服务端新版凭据；原配置已逐字段校验"
	default:
		return "上游未确认账号可用，旧号保留；请检查席位、原料或稍后巡查"
	}
}
