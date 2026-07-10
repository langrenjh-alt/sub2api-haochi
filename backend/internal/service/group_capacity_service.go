package service

import (
	"context"
	"time"
)

// GroupCapacitySummary holds aggregated capacity for a single group.
type GroupCapacitySummary struct {
	GroupID         int64 `json:"group_id"`
	ConcurrencyUsed int   `json:"concurrency_used"`
	ConcurrencyMax  int   `json:"concurrency_max"`
	SessionsUsed    int   `json:"sessions_used"`
	SessionsMax     int   `json:"sessions_max"`
	RPMUsed         int   `json:"rpm_used"`
	RPMMax          int   `json:"rpm_max"`
}

// GroupAccountCapacityRow is the lightweight account projection needed for
// capacity summary aggregation.
type GroupAccountCapacityRow struct {
	GroupID             int64
	AccountID           int64
	Concurrency         int
	Extra               map[string]any
	SessionWindowStart  *time.Time
	SessionWindowEnd    *time.Time
	SessionWindowStatus string
}

// PublicCapacityAccountRow is the account projection used by the user-facing
// public capacity pool. It intentionally excludes credentials and proxy data.
type PublicCapacityAccountRow struct {
	GroupID                int64
	AccountID              int64
	Platform               string
	Type                   string
	Status                 string
	Schedulable            bool
	Concurrency            int
	Extra                  map[string]any
	ExpiresAt              *time.Time
	AutoPauseOnExpired     bool
	RateLimitResetAt       *time.Time
	OverloadUntil          *time.Time
	TempUnschedulableUntil *time.Time
	SessionWindowStart     *time.Time
	SessionWindowEnd       *time.Time
	SessionWindowStatus    string
}

type PublicCapacityPool struct {
	UpdatedAt time.Time                    `json:"updated_at"`
	Summary   PublicCapacityPoolSummary    `json:"summary"`
	Groups    []PublicCapacityGroupSummary `json:"groups"`
}

type PublicCapacityPoolSummary struct {
	GroupTotal           int                         `json:"group_total"`
	AccountTotal         int                         `json:"account_total"`
	ActiveAccounts       int                         `json:"active_accounts"`
	AvailableAccounts    int                         `json:"available_accounts"`
	RateLimitedAccounts  int                         `json:"rate_limited_accounts"`
	QuotaLimitedAccounts int                         `json:"quota_limited_accounts"`
	ErrorAccounts        int                         `json:"error_accounts"`
	DisabledAccounts     int                         `json:"disabled_accounts"`
	StatusCounts         PublicCapacityStatusCounts  `json:"status_counts"`
	GroupHealthCounts    PublicCapacityHealthCounts  `json:"group_health_counts"`
	Capacity             PublicCapacityLimitSnapshot `json:"capacity"`
}

type PublicCapacityGroupSummary struct {
	GroupID           int64                       `json:"group_id"`
	GroupName         string                      `json:"group_name"`
	Platform          string                      `json:"platform"`
	Status            string                      `json:"status"`
	AccountTotal      int                         `json:"account_total"`
	ActiveAccounts    int                         `json:"active_accounts"`
	AvailableAccounts int                         `json:"available_accounts"`
	StatusCounts      PublicCapacityStatusCounts  `json:"status_counts"`
	Capacity          PublicCapacityLimitSnapshot `json:"capacity"`
	Window5h          PublicCapacityWindowSummary `json:"window_5h"`
	Window7d          PublicCapacityWindowSummary `json:"window_7d"`
}

type PublicCapacityStatusCounts struct {
	Normal       int `json:"normal"`
	RateLimited  int `json:"rate_limited"`
	QuotaLimited int `json:"quota_limited"`
	Error        int `json:"error"`
	Disabled     int `json:"disabled"`
}

type PublicCapacityHealthCounts struct {
	Normal        int `json:"normal"`
	Degraded      int `json:"degraded"`
	ResourceTight int `json:"resource_tight"`
	Unavailable   int `json:"unavailable"`
}

type PublicCapacityLimitSnapshot struct {
	Concurrency PublicCapacityLimit `json:"concurrency"`
	Sessions    PublicCapacityLimit `json:"sessions"`
	RPM         PublicCapacityLimit `json:"rpm"`
}

type PublicCapacityLimit struct {
	Used      int `json:"used"`
	Max       int `json:"max"`
	Available int `json:"available"`
}

type PublicCapacityWindowSummary struct {
	Label             string  `json:"label"`
	TrackedAccounts   int     `json:"tracked_accounts"`
	AvailableAccounts int     `json:"available_accounts"`
	UsedPercent       float64 `json:"used_percent"`
	RemainingCapacity float64 `json:"remaining_capacity"`
}

type groupCapacityActiveGroupIDLister interface {
	ListActiveIDs(ctx context.Context) ([]int64, error)
}

type groupCapacityAccountLister interface {
	ListSchedulableCapacityByGroupIDs(ctx context.Context, groupIDs []int64) ([]GroupAccountCapacityRow, error)
}

type publicCapacityPoolAccountLister interface {
	ListPublicCapacityPoolAccountsByGroupIDs(ctx context.Context, groupIDs []int64) ([]PublicCapacityAccountRow, error)
}

// GroupCapacityService aggregates per-group capacity from runtime data.
type GroupCapacityService struct {
	accountRepo        AccountRepository
	groupRepo          GroupRepository
	concurrencyService *ConcurrencyService
	sessionLimitCache  SessionLimitCache
	rpmCache           RPMCache
}

// NewGroupCapacityService creates a new GroupCapacityService.
func NewGroupCapacityService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	concurrencyService *ConcurrencyService,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
) *GroupCapacityService {
	return &GroupCapacityService{
		accountRepo:        accountRepo,
		groupRepo:          groupRepo,
		concurrencyService: concurrencyService,
		sessionLimitCache:  sessionLimitCache,
		rpmCache:           rpmCache,
	}
}

// GetAllGroupCapacity returns capacity summary for all active groups.
func (s *GroupCapacityService) GetAllGroupCapacity(ctx context.Context) ([]GroupCapacitySummary, error) {
	groupIDs, err := s.listActiveGroupIDs(ctx)
	if err != nil {
		return nil, err
	}

	if lister, ok := s.accountRepo.(groupCapacityAccountLister); ok {
		return s.getGroupCapacitiesBatch(ctx, groupIDs, lister)
	}

	return s.getGroupCapacitiesSequential(ctx, groupIDs), nil
}

// GetPublicCapacityPool returns user-safe capacity/status summaries for active
// public standard groups.
func (s *GroupCapacityService) GetPublicCapacityPool(ctx context.Context) (*PublicCapacityPool, error) {
	groups, err := s.listPublicStandardGroups(ctx)
	if err != nil {
		return nil, err
	}

	pool := &PublicCapacityPool{
		UpdatedAt: time.Now().UTC(),
		Groups:    make([]PublicCapacityGroupSummary, 0, len(groups)),
	}
	pool.Summary.GroupTotal = len(groups)

	if len(groups) == 0 {
		return pool, nil
	}

	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
		pool.Groups = append(pool.Groups, PublicCapacityGroupSummary{
			GroupID:   groups[i].ID,
			GroupName: groups[i].Name,
			Platform:  groups[i].Platform,
			Status:    "unavailable",
			Window5h:  PublicCapacityWindowSummary{Label: "5h"},
			Window7d:  PublicCapacityWindowSummary{Label: "7d"},
		})
	}

	groupIndex := make(map[int64]int, len(groupIDs))
	for i, id := range groupIDs {
		groupIndex[id] = i
	}

	rows, err := s.listPublicCapacityRows(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		s.finalizePublicCapacityPool(pool)
		return pool, nil
	}

	accountIDs, accountIDsWithSessions := publicCapacityRuntimeAccountIDs(rows)

	concurrencyMap := map[int64]int{}
	if s.concurrencyService != nil && len(accountIDs) > 0 {
		concurrencyMap, _ = s.concurrencyService.GetAccountConcurrencyBatch(ctx, accountIDs)
	}

	sessionTimeouts := make(map[int64]time.Duration)
	for _, row := range rows {
		acc := publicCapacityAccountFromRow(row)
		if acc.GetMaxSessions() <= 0 {
			continue
		}
		timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		sessionTimeouts[acc.ID] = timeout
	}

	sessionsMap := map[int64]int{}
	if s.sessionLimitCache != nil && len(accountIDsWithSessions) > 0 {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, accountIDsWithSessions, sessionTimeouts)
	}

	rpmAccountIDs := publicCapacityRuntimeAccountIDsByLimit(rows, func(acc *Account) bool {
		return acc.GetBaseRPM() > 0
	})
	rpmMap := map[int64]int{}
	if s.rpmCache != nil && len(rpmAccountIDs) > 0 {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, rpmAccountIDs)
	}

	windowCostAccountIDs := publicCapacityRuntimeAccountIDsByLimit(rows, func(acc *Account) bool {
		return acc.GetWindowCostLimit() > 0
	})
	windowCostMap := map[int64]float64{}
	if s.sessionLimitCache != nil && len(windowCostAccountIDs) > 0 {
		windowCostMap, _ = s.sessionLimitCache.GetWindowCostBatch(ctx, windowCostAccountIDs)
	}

	seenGroupAccount := make(map[groupCapacityAccountRef]struct{}, len(rows))
	for _, row := range rows {
		idx, ok := groupIndex[row.GroupID]
		if !ok || row.AccountID <= 0 {
			continue
		}
		ref := groupCapacityAccountRef{groupID: row.GroupID, accountID: row.AccountID}
		if _, ok := seenGroupAccount[ref]; ok {
			continue
		}
		seenGroupAccount[ref] = struct{}{}

		acc := publicCapacityAccountFromRow(row)
		runtime := publicCapacityRuntime{
			concurrency: concurrencyMap[row.AccountID],
			sessions:    sessionsMap[row.AccountID],
			rpm:         rpmMap[row.AccountID],
			windowCost:  windowCostMap[row.AccountID],
		}
		status := classifyPublicCapacityAccount(acc, runtime, pool.UpdatedAt)
		applyPublicCapacityAccount(&pool.Groups[idx], acc, status, runtime, pool.UpdatedAt)
	}

	s.finalizePublicCapacityPool(pool)
	return pool, nil
}

func (s *GroupCapacityService) listPublicStandardGroups(ctx context.Context) ([]Group, error) {
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if g.IsExclusive {
			continue
		}
		if g.SubscriptionType != "" && g.SubscriptionType != SubscriptionTypeStandard {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *GroupCapacityService) listPublicCapacityRows(ctx context.Context, groupIDs []int64) ([]PublicCapacityAccountRow, error) {
	if lister, ok := s.accountRepo.(publicCapacityPoolAccountLister); ok {
		return lister.ListPublicCapacityPoolAccountsByGroupIDs(ctx, groupIDs)
	}

	rows := make([]PublicCapacityAccountRow, 0)
	for _, groupID := range groupIDs {
		accounts, err := s.accountRepo.ListByGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		for i := range accounts {
			acc := &accounts[i]
			rows = append(rows, PublicCapacityAccountRow{
				GroupID:                groupID,
				AccountID:              acc.ID,
				Platform:               acc.Platform,
				Type:                   acc.Type,
				Status:                 acc.Status,
				Schedulable:            acc.Schedulable,
				Concurrency:            acc.Concurrency,
				Extra:                  copyPublicCapacityExtra(acc.Extra),
				ExpiresAt:              acc.ExpiresAt,
				AutoPauseOnExpired:     acc.AutoPauseOnExpired,
				RateLimitResetAt:       acc.RateLimitResetAt,
				OverloadUntil:          acc.OverloadUntil,
				TempUnschedulableUntil: acc.TempUnschedulableUntil,
				SessionWindowStart:     acc.SessionWindowStart,
				SessionWindowEnd:       acc.SessionWindowEnd,
				SessionWindowStatus:    acc.SessionWindowStatus,
			})
		}
	}
	return rows, nil
}

func (s *GroupCapacityService) listActiveGroupIDs(ctx context.Context) ([]int64, error) {
	if lister, ok := s.groupRepo.(groupCapacityActiveGroupIDLister); ok {
		return lister.ListActiveIDs(ctx)
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
	}
	return groupIDs, nil
}

func (s *GroupCapacityService) getGroupCapacitiesSequential(ctx context.Context, groupIDs []int64) []GroupCapacitySummary {
	results := make([]GroupCapacitySummary, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		cap, err := s.getGroupCapacity(ctx, groupID)
		if err != nil {
			// Skip groups with errors, return partial results
			continue
		}
		cap.GroupID = groupID
		results = append(results, cap)
	}
	return results
}

type groupCapacityAccountRef struct {
	groupID   int64
	accountID int64
}

type publicCapacityAccountStatus string

const (
	publicCapacityStatusNormal       publicCapacityAccountStatus = "normal"
	publicCapacityStatusRateLimited  publicCapacityAccountStatus = "rate_limited"
	publicCapacityStatusQuotaLimited publicCapacityAccountStatus = "quota_limited"
	publicCapacityStatusError        publicCapacityAccountStatus = "error"
	publicCapacityStatusDisabled     publicCapacityAccountStatus = "disabled"
)

type publicCapacityRuntime struct {
	concurrency int
	sessions    int
	rpm         int
	windowCost  float64
}

func publicCapacityAccountFromRow(row PublicCapacityAccountRow) *Account {
	return &Account{
		ID:                     row.AccountID,
		Platform:               row.Platform,
		Type:                   row.Type,
		Status:                 row.Status,
		Schedulable:            row.Schedulable,
		Concurrency:            row.Concurrency,
		Extra:                  copyPublicCapacityExtra(row.Extra),
		ExpiresAt:              row.ExpiresAt,
		AutoPauseOnExpired:     row.AutoPauseOnExpired,
		RateLimitResetAt:       row.RateLimitResetAt,
		OverloadUntil:          row.OverloadUntil,
		TempUnschedulableUntil: row.TempUnschedulableUntil,
		SessionWindowStart:     row.SessionWindowStart,
		SessionWindowEnd:       row.SessionWindowEnd,
		SessionWindowStatus:    row.SessionWindowStatus,
	}
}

func copyPublicCapacityExtra(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func publicCapacityRuntimeAccountIDs(rows []PublicCapacityAccountRow) ([]int64, []int64) {
	allSeen := make(map[int64]struct{}, len(rows))
	sessionSeen := make(map[int64]struct{})
	all := make([]int64, 0, len(rows))
	sessions := make([]int64, 0)
	for _, row := range rows {
		if row.AccountID <= 0 {
			continue
		}
		if _, ok := allSeen[row.AccountID]; !ok {
			allSeen[row.AccountID] = struct{}{}
			all = append(all, row.AccountID)
		}
		acc := publicCapacityAccountFromRow(row)
		if acc.GetMaxSessions() <= 0 {
			continue
		}
		if _, ok := sessionSeen[row.AccountID]; ok {
			continue
		}
		sessionSeen[row.AccountID] = struct{}{}
		sessions = append(sessions, row.AccountID)
	}
	return all, sessions
}

func publicCapacityRuntimeAccountIDsByLimit(rows []PublicCapacityAccountRow, include func(*Account) bool) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, row := range rows {
		if row.AccountID <= 0 {
			continue
		}
		if _, ok := seen[row.AccountID]; ok {
			continue
		}
		acc := publicCapacityAccountFromRow(row)
		if !include(acc) {
			continue
		}
		seen[row.AccountID] = struct{}{}
		ids = append(ids, row.AccountID)
	}
	return ids
}

func classifyPublicCapacityAccount(acc *Account, runtime publicCapacityRuntime, now time.Time) publicCapacityAccountStatus {
	if acc == nil {
		return publicCapacityStatusDisabled
	}
	if acc.Status == StatusError {
		return publicCapacityStatusError
	}
	if acc.Status != StatusActive || !acc.Schedulable || accountExpiredAt(acc, now) {
		return publicCapacityStatusDisabled
	}
	if acc.RateLimitResetAt != nil && now.Before(*acc.RateLimitResetAt) {
		return publicCapacityStatusRateLimited
	}
	if acc.OverloadUntil != nil && now.Before(*acc.OverloadUntil) {
		return publicCapacityStatusQuotaLimited
	}
	if acc.TempUnschedulableUntil != nil && now.Before(*acc.TempUnschedulableUntil) {
		return publicCapacityStatusQuotaLimited
	}
	if acc.IsAPIKeyOrBedrock() && acc.IsQuotaExceeded() {
		return publicCapacityStatusQuotaLimited
	}
	if acc.Concurrency > 0 && runtime.concurrency >= acc.Concurrency {
		return publicCapacityStatusQuotaLimited
	}
	if maxSessions := acc.GetMaxSessions(); maxSessions > 0 && runtime.sessions >= maxSessions {
		return publicCapacityStatusQuotaLimited
	}
	if acc.CheckRPMSchedulability(runtime.rpm) != WindowCostSchedulable {
		return publicCapacityStatusQuotaLimited
	}
	if acc.CheckWindowCostSchedulability(runtime.windowCost) != WindowCostSchedulable {
		return publicCapacityStatusQuotaLimited
	}
	if publicCapacityUsagePercent(acc, "codex_5h_used_percent", runtime.windowCost) >= 100 {
		return publicCapacityStatusQuotaLimited
	}
	if publicCapacityUsagePercent(acc, "codex_7d_used_percent", 0) >= 100 {
		return publicCapacityStatusQuotaLimited
	}
	return publicCapacityStatusNormal
}

func accountExpiredAt(acc *Account, now time.Time) bool {
	return acc.AutoPauseOnExpired && acc.ExpiresAt != nil && !now.Before(*acc.ExpiresAt)
}

func applyPublicCapacityAccount(group *PublicCapacityGroupSummary, acc *Account, status publicCapacityAccountStatus, runtime publicCapacityRuntime, now time.Time) {
	group.AccountTotal++
	if acc.Status == StatusActive && !accountExpiredAt(acc, now) {
		group.ActiveAccounts++
	}
	if status == publicCapacityStatusNormal {
		group.AvailableAccounts++
	}

	switch status {
	case publicCapacityStatusNormal:
		group.StatusCounts.Normal++
	case publicCapacityStatusRateLimited:
		group.StatusCounts.RateLimited++
	case publicCapacityStatusQuotaLimited:
		group.StatusCounts.QuotaLimited++
	case publicCapacityStatusError:
		group.StatusCounts.Error++
	case publicCapacityStatusDisabled:
		group.StatusCounts.Disabled++
	}

	group.Capacity.Concurrency.Max += positiveInt(acc.Concurrency)
	group.Capacity.Concurrency.Used += positiveInt(runtime.concurrency)

	maxSessions := acc.GetMaxSessions()
	if maxSessions > 0 {
		group.Capacity.Sessions.Max += maxSessions
		group.Capacity.Sessions.Used += positiveInt(runtime.sessions)
	}

	baseRPM := acc.GetBaseRPM()
	if baseRPM > 0 {
		group.Capacity.RPM.Max += baseRPM
		group.Capacity.RPM.Used += positiveInt(runtime.rpm)
	}

	if status == publicCapacityStatusNormal {
		applyPublicCapacityWindow(&group.Window5h, acc, "codex_5h_used_percent", runtime.windowCost)
		applyPublicCapacityWindow(&group.Window7d, acc, "codex_7d_used_percent", 0)
	}
}

func applyPublicCapacityWindow(window *PublicCapacityWindowSummary, acc *Account, key string, currentWindowCost float64) {
	percent, ok := publicCapacityUsagePercentWithPresence(acc, key, currentWindowCost)
	if !ok {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	window.TrackedAccounts++
	if percent < 100 {
		window.AvailableAccounts++
	}
	window.UsedPercent += percent
	window.RemainingCapacity += 1 - percent/100
}

func publicCapacityUsagePercent(acc *Account, key string, currentWindowCost float64) float64 {
	percent, _ := publicCapacityUsagePercentWithPresence(acc, key, currentWindowCost)
	return percent
}

func publicCapacityUsagePercentWithPresence(acc *Account, key string, currentWindowCost float64) (float64, bool) {
	if acc == nil {
		return 0, false
	}
	if acc.Extra != nil {
		if value, ok := acc.Extra[key]; ok {
			return parseExtraFloat64(value), true
		}
	}
	if key == "codex_5h_used_percent" {
		limit := acc.GetWindowCostLimit()
		if limit > 0 {
			return currentWindowCost / limit * 100, true
		}
	}
	return 0, false
}

func (s *GroupCapacityService) finalizePublicCapacityPool(pool *PublicCapacityPool) {
	for i := range pool.Groups {
		group := &pool.Groups[i]
		group.Capacity.Concurrency.Available = nonNegative(group.Capacity.Concurrency.Max - group.Capacity.Concurrency.Used)
		group.Capacity.Sessions.Available = nonNegative(group.Capacity.Sessions.Max - group.Capacity.Sessions.Used)
		group.Capacity.RPM.Available = nonNegative(group.Capacity.RPM.Max - group.Capacity.RPM.Used)
		finalizePublicCapacityWindow(&group.Window5h)
		finalizePublicCapacityWindow(&group.Window7d)
		group.Status = publicCapacityGroupHealth(*group)

		pool.Summary.AccountTotal += group.AccountTotal
		pool.Summary.ActiveAccounts += group.ActiveAccounts
		pool.Summary.AvailableAccounts += group.AvailableAccounts
		pool.Summary.StatusCounts.Normal += group.StatusCounts.Normal
		pool.Summary.StatusCounts.RateLimited += group.StatusCounts.RateLimited
		pool.Summary.StatusCounts.QuotaLimited += group.StatusCounts.QuotaLimited
		pool.Summary.StatusCounts.Error += group.StatusCounts.Error
		pool.Summary.StatusCounts.Disabled += group.StatusCounts.Disabled
		pool.Summary.RateLimitedAccounts += group.StatusCounts.RateLimited
		pool.Summary.QuotaLimitedAccounts += group.StatusCounts.QuotaLimited
		pool.Summary.ErrorAccounts += group.StatusCounts.Error
		pool.Summary.DisabledAccounts += group.StatusCounts.Disabled
		pool.Summary.Capacity.Concurrency.Used += group.Capacity.Concurrency.Used
		pool.Summary.Capacity.Concurrency.Max += group.Capacity.Concurrency.Max
		pool.Summary.Capacity.Sessions.Used += group.Capacity.Sessions.Used
		pool.Summary.Capacity.Sessions.Max += group.Capacity.Sessions.Max
		pool.Summary.Capacity.RPM.Used += group.Capacity.RPM.Used
		pool.Summary.Capacity.RPM.Max += group.Capacity.RPM.Max

		switch group.Status {
		case "normal":
			pool.Summary.GroupHealthCounts.Normal++
		case "resource_tight":
			pool.Summary.GroupHealthCounts.ResourceTight++
		case "degraded":
			pool.Summary.GroupHealthCounts.Degraded++
		default:
			pool.Summary.GroupHealthCounts.Unavailable++
		}
	}
	pool.Summary.Capacity.Concurrency.Available = nonNegative(pool.Summary.Capacity.Concurrency.Max - pool.Summary.Capacity.Concurrency.Used)
	pool.Summary.Capacity.Sessions.Available = nonNegative(pool.Summary.Capacity.Sessions.Max - pool.Summary.Capacity.Sessions.Used)
	pool.Summary.Capacity.RPM.Available = nonNegative(pool.Summary.Capacity.RPM.Max - pool.Summary.Capacity.RPM.Used)
}

func finalizePublicCapacityWindow(window *PublicCapacityWindowSummary) {
	if window.TrackedAccounts == 0 {
		return
	}
	window.UsedPercent = window.UsedPercent / float64(window.TrackedAccounts)
}

func publicCapacityGroupHealth(group PublicCapacityGroupSummary) string {
	if group.AccountTotal == 0 || group.AvailableAccounts == 0 {
		return "unavailable"
	}
	if group.Capacity.Concurrency.Max > 0 {
		usedRatio := float64(group.Capacity.Concurrency.Used) / float64(group.Capacity.Concurrency.Max)
		if usedRatio >= 0.9 {
			return "resource_tight"
		}
	}
	if group.Window5h.TrackedAccounts > 0 && group.Window5h.UsedPercent >= 85 {
		return "resource_tight"
	}
	if group.Window7d.TrackedAccounts > 0 && group.Window7d.UsedPercent >= 85 {
		return "resource_tight"
	}
	if group.StatusCounts.RateLimited > 0 || group.StatusCounts.QuotaLimited > 0 || group.StatusCounts.Error > 0 || group.StatusCounts.Disabled > 0 {
		return "degraded"
	}
	return "normal"
}

func positiveInt(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func nonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func (s *GroupCapacityService) getGroupCapacitiesBatch(ctx context.Context, groupIDs []int64, lister groupCapacityAccountLister) ([]GroupCapacitySummary, error) {
	results := make([]GroupCapacitySummary, len(groupIDs))
	groupIndex := make(map[int64]int, len(groupIDs))
	for i, groupID := range groupIDs {
		results[i].GroupID = groupID
		groupIndex[groupID] = i
	}
	if len(groupIDs) == 0 {
		return results, nil
	}

	rows, err := lister.ListSchedulableCapacityByGroupIDs(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return results, nil
	}

	refs := make([]groupCapacityAccountRef, 0, len(rows))
	seenGroupAccount := make(map[groupCapacityAccountRef]struct{}, len(rows))
	accountIDSet := make(map[int64]struct{}, len(rows))
	accountIDs := make([]int64, 0, len(rows))
	sessionTimeouts := make(map[int64]time.Duration)

	for _, row := range rows {
		idx, ok := groupIndex[row.GroupID]
		if !ok || row.AccountID <= 0 {
			continue
		}

		ref := groupCapacityAccountRef{groupID: row.GroupID, accountID: row.AccountID}
		if _, ok := seenGroupAccount[ref]; ok {
			continue
		}
		seenGroupAccount[ref] = struct{}{}
		refs = append(refs, ref)

		if _, ok := accountIDSet[row.AccountID]; !ok {
			accountIDSet[row.AccountID] = struct{}{}
			accountIDs = append(accountIDs, row.AccountID)
		}

		acc := Account{
			ID:                  row.AccountID,
			Concurrency:         row.Concurrency,
			Extra:               row.Extra,
			SessionWindowStart:  row.SessionWindowStart,
			SessionWindowEnd:    row.SessionWindowEnd,
			SessionWindowStatus: row.SessionWindowStatus,
		}

		results[idx].ConcurrencyMax += acc.Concurrency

		if maxSessions := acc.GetMaxSessions(); maxSessions > 0 {
			results[idx].SessionsMax += maxSessions
			timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
			if timeout <= 0 {
				timeout = 5 * time.Minute
			}
			sessionTimeouts[acc.ID] = timeout
		}

		if rpm := acc.GetBaseRPM(); rpm > 0 {
			results[idx].RPMMax += rpm
		}
	}

	if len(accountIDs) == 0 {
		return results, nil
	}

	concurrencyMap := map[int64]int{}
	if s.concurrencyService != nil {
		concurrencyMap, _ = s.concurrencyService.GetAccountConcurrencyBatch(ctx, accountIDs)
	}

	sessionAccountIDs := accountIDsForGroupsWithLimit(refs, groupIndex, results, func(summary GroupCapacitySummary) bool {
		return summary.SessionsMax > 0
	})
	var sessionsMap map[int64]int
	if len(sessionAccountIDs) > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, sessionAccountIDs, sessionTimeouts)
	}

	rpmAccountIDs := accountIDsForGroupsWithLimit(refs, groupIndex, results, func(summary GroupCapacitySummary) bool {
		return summary.RPMMax > 0
	})
	var rpmMap map[int64]int
	if len(rpmAccountIDs) > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, rpmAccountIDs)
	}

	for _, ref := range refs {
		idx := groupIndex[ref.groupID]
		results[idx].ConcurrencyUsed += concurrencyMap[ref.accountID]
		if sessionsMap != nil && results[idx].SessionsMax > 0 {
			results[idx].SessionsUsed += sessionsMap[ref.accountID]
		}
		if rpmMap != nil && results[idx].RPMMax > 0 {
			results[idx].RPMUsed += rpmMap[ref.accountID]
		}
	}
	return results, nil
}

func accountIDsForGroupsWithLimit(refs []groupCapacityAccountRef, groupIndex map[int64]int, summaries []GroupCapacitySummary, include func(GroupCapacitySummary) bool) []int64 {
	seen := make(map[int64]struct{})
	accountIDs := make([]int64, 0)
	for _, ref := range refs {
		idx, ok := groupIndex[ref.groupID]
		if !ok || !include(summaries[idx]) {
			continue
		}
		if _, ok := seen[ref.accountID]; ok {
			continue
		}
		seen[ref.accountID] = struct{}{}
		accountIDs = append(accountIDs, ref.accountID)
	}
	return accountIDs
}

func (s *GroupCapacityService) getGroupCapacity(ctx context.Context, groupID int64) (GroupCapacitySummary, error) {
	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return GroupCapacitySummary{}, err
	}
	if len(accounts) == 0 {
		return GroupCapacitySummary{}, nil
	}

	// Collect account IDs and config values
	accountIDs := make([]int64, 0, len(accounts))
	sessionTimeouts := make(map[int64]time.Duration)
	var concurrencyMax, sessionsMax, rpmMax int

	for i := range accounts {
		acc := &accounts[i]
		accountIDs = append(accountIDs, acc.ID)
		concurrencyMax += acc.Concurrency

		if ms := acc.GetMaxSessions(); ms > 0 {
			sessionsMax += ms
			timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
			if timeout <= 0 {
				timeout = 5 * time.Minute
			}
			sessionTimeouts[acc.ID] = timeout
		}

		if rpm := acc.GetBaseRPM(); rpm > 0 {
			rpmMax += rpm
		}
	}

	// Batch query runtime data from Redis
	concurrencyMap, _ := s.concurrencyService.GetAccountConcurrencyBatch(ctx, accountIDs)

	var sessionsMap map[int64]int
	if sessionsMax > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, accountIDs, sessionTimeouts)
	}

	var rpmMap map[int64]int
	if rpmMax > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, accountIDs)
	}

	// Aggregate
	var concurrencyUsed, sessionsUsed, rpmUsed int
	for _, id := range accountIDs {
		concurrencyUsed += concurrencyMap[id]
		if sessionsMap != nil {
			sessionsUsed += sessionsMap[id]
		}
		if rpmMap != nil {
			rpmUsed += rpmMap[id]
		}
	}

	return GroupCapacitySummary{
		ConcurrencyUsed: concurrencyUsed,
		ConcurrencyMax:  concurrencyMax,
		SessionsUsed:    sessionsUsed,
		SessionsMax:     sessionsMax,
		RPMUsed:         rpmUsed,
		RPMMax:          rpmMax,
	}, nil
}
