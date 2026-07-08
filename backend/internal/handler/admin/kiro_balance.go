package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/sync/errgroup"
)

const (
	kiroRSBalanceRequestTimeout = 900 * time.Millisecond
	kiroRSBalanceBatchTimeout   = 1500 * time.Millisecond
	kiroRSBalanceSuccessTTL     = 60 * time.Second
	kiroRSBalanceErrorTTL       = 15 * time.Second
)

// KiroRSBalanceInfo is intentionally small and safe for the admin account list.
// Secrets stay in account credentials and are never returned to the frontend.
type KiroRSBalanceInfo struct {
	SubscriptionTitle string  `json:"subscription_title,omitempty"`
	CurrentUsage      float64 `json:"current_usage"`
	UsageLimit        float64 `json:"usage_limit"`
	Remaining         float64 `json:"remaining"`
	UsagePercentage   float64 `json:"usage_percentage"`
	CredentialID      string  `json:"credential_id,omitempty"`
	UpdatedAt         string  `json:"updated_at,omitempty"`
	Error             string  `json:"error,omitempty"`
}

type kiroRSBalanceConfig struct {
	BaseURL      string
	AdminKey     string
	CredentialID string
}

type kiroRSBalanceCacheEntry struct {
	info      *KiroRSBalanceInfo
	expiresAt time.Time
}

var (
	kiroRSBalanceCache sync.Map
	kiroRSBalanceHTTP  = &http.Client{Timeout: kiroRSBalanceRequestTimeout}
)

func getKiroRSBalancesForAccounts(ctx context.Context, accounts []service.Account) map[int64]*KiroRSBalanceInfo {
	type candidate struct {
		accountID int64
		config    kiroRSBalanceConfig
	}

	candidates := make([]candidate, 0)
	for i := range accounts {
		if cfg, ok := buildKiroRSBalanceConfig(&accounts[i]); ok {
			candidates = append(candidates, candidate{accountID: accounts[i].ID, config: cfg})
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, kiroRSBalanceBatchTimeout)
	defer cancel()

	out := make(map[int64]*KiroRSBalanceInfo, len(candidates))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for _, item := range candidates {
		item := item
		g.Go(func() error {
			info := fetchKiroRSBalance(gctx, item.config)
			mu.Lock()
			out[item.accountID] = info
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func getKiroRSBalanceForAccount(ctx context.Context, account *service.Account) *KiroRSBalanceInfo {
	cfg, ok := buildKiroRSBalanceConfig(account)
	if !ok {
		return nil
	}
	return fetchKiroRSBalance(ctx, cfg)
}

func buildKiroRSBalanceConfig(account *service.Account) (kiroRSBalanceConfig, bool) {
	if account == nil {
		return kiroRSBalanceConfig{}, false
	}

	baseURL := firstAccountString(account, false, "kiro_rs_base_url", "kiro_base_url", "base_url")
	adminKey := firstAccountString(account, true, "kiro_rs_admin_key", "kiro_admin_key", "admin_key")
	credentialID := firstAccountString(account, false, "kiro_rs_credential_id", "kiro_credential_id", "credential_id")
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(adminKey) == "" {
		return kiroRSBalanceConfig{}, false
	}

	source := strings.ToLower(strings.TrimSpace(firstAccountString(account, false, "source")))
	lowerBaseURL := strings.ToLower(baseURL)
	enabled := firstAccountBool(account, "kiro_rs_balance_enabled", "kiro_balance_enabled")
	isKiroAccount := enabled ||
		credentialID != "" ||
		strings.Contains(source, "kiro") ||
		strings.Contains(lowerBaseURL, "kiro") ||
		strings.Contains(lowerBaseURL, ":8990")
	if !isKiroAccount {
		return kiroRSBalanceConfig{}, false
	}
	if credentialID == "" {
		credentialID = "1"
	}

	return kiroRSBalanceConfig{
		BaseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		AdminKey:     strings.TrimSpace(adminKey),
		CredentialID: strings.TrimSpace(credentialID),
	}, true
}

func fetchKiroRSBalance(ctx context.Context, cfg kiroRSBalanceConfig) *KiroRSBalanceInfo {
	cacheKey := kiroRSBalanceCacheKey(cfg)
	now := time.Now()
	if cached, ok := kiroRSBalanceCache.Load(cacheKey); ok {
		entry := cached.(kiroRSBalanceCacheEntry)
		if now.Before(entry.expiresAt) {
			return cloneKiroRSBalanceInfo(entry.info)
		}
	}

	info := requestKiroRSBalance(ctx, cfg)
	ttl := kiroRSBalanceSuccessTTL
	if info == nil || info.Error != "" {
		ttl = kiroRSBalanceErrorTTL
	}
	kiroRSBalanceCache.Store(cacheKey, kiroRSBalanceCacheEntry{
		info:      cloneKiroRSBalanceInfo(info),
		expiresAt: now.Add(ttl),
	})
	return info
}

func requestKiroRSBalance(ctx context.Context, cfg kiroRSBalanceConfig) *KiroRSBalanceInfo {
	ctx, cancel := context.WithTimeout(ctx, kiroRSBalanceRequestTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("%s/api/admin/credentials/%s/balance", cfg.BaseURL, url.PathEscape(cfg.CredentialID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return kiroRSBalanceError(cfg, "invalid kiro-rs balance url")
	}
	req.Header.Set("x-api-key", cfg.AdminKey)
	req.Header.Set("Accept", "application/json")

	resp, err := kiroRSBalanceHTTP.Do(req)
	if err != nil {
		return kiroRSBalanceError(cfg, "kiro-rs balance request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return kiroRSBalanceError(cfg, fmt.Sprintf("kiro-rs balance http %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return kiroRSBalanceError(cfg, "kiro-rs balance read failed")
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return kiroRSBalanceError(cfg, "kiro-rs balance json invalid")
	}
	if nested, ok := payload["data"].(map[string]any); ok {
		payload = nested
	}

	info := &KiroRSBalanceInfo{
		SubscriptionTitle: firstJSONStr(payload, "subscriptionTitle", "subscription_title", "title"),
		CurrentUsage:      firstJSONFloat(payload, "currentUsage", "current_usage", "used", "usage"),
		UsageLimit:        firstJSONFloat(payload, "usageLimit", "usage_limit", "limit"),
		Remaining:         firstJSONFloat(payload, "remaining", "remain"),
		UsagePercentage:   firstJSONFloat(payload, "usagePercentage", "usage_percentage", "percent"),
		CredentialID:      cfg.CredentialID,
		UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	if info.Remaining == 0 && info.UsageLimit > 0 {
		info.Remaining = info.UsageLimit - info.CurrentUsage
	}
	if info.UsagePercentage == 0 && info.UsageLimit > 0 {
		info.UsagePercentage = info.CurrentUsage / info.UsageLimit * 100
	}
	return info
}

func kiroRSBalanceError(cfg kiroRSBalanceConfig, message string) *KiroRSBalanceInfo {
	return &KiroRSBalanceInfo{
		CredentialID: cfg.CredentialID,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Error:        message,
	}
}

func firstAccountString(account *service.Account, credentialsOnly bool, keys ...string) string {
	if account == nil {
		return ""
	}
	for _, key := range keys {
		if v := strings.TrimSpace(account.GetCredential(key)); v != "" {
			return v
		}
		if credentialsOnly || account.Extra == nil {
			continue
		}
		if v := strings.TrimSpace(stringFromAny(account.Extra[key])); v != "" {
			return v
		}
	}
	return ""
}

func firstAccountBool(account *service.Account, keys ...string) bool {
	if account == nil {
		return false
	}
	for _, key := range keys {
		if boolFromAny(account.Credentials[key]) {
			return true
		}
		if boolFromAny(account.Extra[key]) {
			return true
		}
	}
	return false
}

func firstJSONStr(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(stringFromAny(payload[key])); v != "" {
			return v
		}
	}
	return ""
}

func firstJSONFloat(payload map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if v, ok := floatFromAny(payload[key]); ok {
			return v
		}
	}
	return 0
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	default:
		return ""
	}
}

func floatFromAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(x, "%")), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(x))
		return err == nil && parsed
	default:
		return false
	}
}

func kiroRSBalanceCacheKey(cfg kiroRSBalanceConfig) string {
	sum := sha256.Sum256([]byte(cfg.BaseURL + "|" + cfg.CredentialID))
	return hex.EncodeToString(sum[:])
}

func cloneKiroRSBalanceInfo(in *KiroRSBalanceInfo) *KiroRSBalanceInfo {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
