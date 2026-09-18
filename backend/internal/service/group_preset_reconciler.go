package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 分组预设收敛器：把 groups.anti_degrade_preset 持续对齐到组内账号。
//
// 设计要点（与 WishTeam5X 的交互是主要约束）：
//   - 每轮都按**当前成员关系**重新取候选，不缓存也不持久化账号 ID。WishTeam5X 复活
//     会软删旧行并插入新 ID 的行，任何"先抓一份 ID 再逐个写"的实现都会在复活后静默
//     写空（BulkUpdate 命中 0 行不报错），这里天然免疫。
//   - 只写 accounts.extra 与 accounts.concurrency，不新增 accounts 列、不加触发器，
//     因此不会破坏复活事务末尾的逐列全等校验。
//   - 每轮每个分组处理有上限（maxPerPass）。已对齐的账号下一轮就不再是候选，
//     所以大分组是"多轮收敛"而不是一轮扫 7000 个账号。
//   - 跨进程没有加 advisory lock：apply/revert 本身幂等且带 updated_at 乐观锁，
//     多副本同时跑只会做重复功、不会写坏数据。单副本部署（systemd 单实例）不受影响；
//     若在 nginx LB 后面跑多副本，建议只在其中一个节点启用本 worker。

const (
	// 每轮每分组最多处理的账号数，控制单轮时长与 DB 压力。
	defaultGroupPresetMaxPerPass = 200
	// 每轮之间的默认间隔。
	defaultGroupPresetInterval = 60 * time.Second
	// 单个账号处理失败后是否继续；连续失败过多则提前结束本轮。
	defaultGroupPresetMaxFailures = 50
)

// GroupPresetSyncFailure 单个账号的收敛失败记录（对外只读展示）。
type GroupPresetSyncFailure struct {
	AccountID int64  `json:"account_id"`
	Reason    string `json:"reason"`
}

// GroupPresetSyncStatus 分组预设的收敛状态快照（内存态，进程重启即重置）。
type GroupPresetSyncStatus struct {
	GroupID        int64                    `json:"group_id"`
	Preset         string                   `json:"preset"`
	Total          int                      `json:"total"`
	Candidates     int                      `json:"candidates"`
	Applied        int                      `json:"applied"`
	Reverted       int                      `json:"reverted"`
	Skipped        int                      `json:"skipped"`
	Failed         int                      `json:"failed"`
	Failures       []GroupPresetSyncFailure `json:"failures,omitempty"`
	LastRunAt      *time.Time               `json:"last_run_at,omitempty"`
	LastDurationMS int64                    `json:"last_duration_ms"`
	Running        bool                     `json:"running"`
}

// GroupAntiDegradeReconciler 分组防降智预设收敛器。
type GroupAntiDegradeReconciler struct {
	groups   GroupRepository
	anti     *AntiDegradeService
	interval time.Duration
	maxPer   int

	mu      sync.Mutex
	status  map[int64]*GroupPresetSyncStatus
	running bool
	stopped bool
}

// NewGroupAntiDegradeReconciler 构造收敛器；groups 或 anti 为空时 worker 不启动。
func NewGroupAntiDegradeReconciler(groups GroupRepository, anti *AntiDegradeService) *GroupAntiDegradeReconciler {
	return &GroupAntiDegradeReconciler{
		groups:   groups,
		anti:     anti,
		interval: defaultGroupPresetInterval,
		maxPer:   defaultGroupPresetMaxPerPass,
		status:   make(map[int64]*GroupPresetSyncStatus),
	}
}

// SetInterval 覆盖轮询间隔（测试与运维调参用）。
func (r *GroupAntiDegradeReconciler) SetInterval(d time.Duration) {
	if r == nil || d <= 0 {
		return
	}
	r.interval = d
}

// Start 启动后台收敛循环。立即跑一轮，之后按 interval 轮询。
func (r *GroupAntiDegradeReconciler) Start(ctx context.Context) {
	if r == nil || r.groups == nil || r.anti == nil {
		logger.LegacyPrintf("service.group_preset", "[GroupPreset] reconciler not started: missing dependencies")
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				logger.LegacyPrintf("service.group_preset", "[GroupPreset] reconciler panic: %v", rec)
			}
		}()

		r.ReconcileAll(ctx)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				r.mu.Lock()
				r.stopped = true
				r.mu.Unlock()
				return
			case <-ticker.C:
				r.ReconcileAll(ctx)
			}
		}
	}()
}

// ReconcileAll 对所有启用预设的分组各跑一轮。导出以便管理端手动触发。
func (r *GroupAntiDegradeReconciler) ReconcileAll(ctx context.Context) {
	if r == nil || r.groups == nil || r.anti == nil {
		return
	}

	// 进程内单飞：一轮没跑完就不再叠加。
	r.mu.Lock()
	if r.running || r.stopped {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	groups, err := r.groups.ListActive(ctx)
	if err != nil {
		logger.LegacyPrintf("service.group_preset", "[GroupPreset] list active groups failed: %v", err)
		return
	}
	for i := range groups {
		if ctx.Err() != nil {
			return
		}
		g := groups[i]
		preset := strings.TrimSpace(g.AntiDegradePreset)
		if preset == "" && !r.hasGroupSourcedMembers(ctx, g.ID) {
			// 既没有分组预设、也没有本分组写过的账号：无需回滚，跳过。
			continue
		}
		if preset != "" {
			if _, err := NormalizeGroupAntiDegradePreset(preset); err != nil {
				logger.LegacyPrintf("service.group_preset", "[GroupPreset] group=%d has invalid preset %q: %v", g.ID, preset, err)
				continue
			}
		}
		r.ReconcileGroup(ctx, g.ID, preset)
	}
}

// hasGroupSourcedMembers 报告分组内是否存在"由本分组写过预设"的账号（关闭方向是否需要工作）。
func (r *GroupAntiDegradeReconciler) hasGroupSourcedMembers(ctx context.Context, groupID int64) bool {
	ids, err := r.groups.ListAntiDegradePresetCandidates(ctx, groupID, "")
	if err != nil {
		logger.LegacyPrintf("service.group_preset", "[GroupPreset] probe group=%d rollback set failed: %v", groupID, err)
		return false
	}
	return len(ids) > 0
}

// ReconcileGroup 对单个分组跑一轮收敛。
// preset 非空 = 套用该预设；preset 为空 = 回滚该分组写入过的预设。
func (r *GroupAntiDegradeReconciler) ReconcileGroup(ctx context.Context, groupID int64, preset string) *GroupPresetSyncStatus {
	status := &GroupPresetSyncStatus{
		GroupID:   groupID,
		Preset:    preset,
		Running:   true,
		LastRunAt: nil,
	}
	if r != nil {
		r.mu.Lock()
		if previous := r.status[groupID]; previous != nil && previous.Running {
			copied := *previous
			copied.Failures = append([]GroupPresetSyncFailure(nil), previous.Failures...)
			r.mu.Unlock()
			return &copied
		}
		// Publish snapshots only: the worker's mutable counters are private.
		initial := *status
		r.status[groupID] = &initial
		r.mu.Unlock()
	}

	start := time.Now()
	defer func() {
		if r == nil {
			return
		}
		status.Running = false
		now := time.Now()
		status.LastRunAt = &now
		status.LastDurationMS = time.Since(start).Milliseconds()
		completed := *status
		completed.Failures = append([]GroupPresetSyncFailure(nil), status.Failures...)
		r.mu.Lock()
		r.status[groupID] = &completed
		r.mu.Unlock()
	}()

	if r == nil || r.groups == nil || r.anti == nil || groupID <= 0 {
		return status
	}

	if total, _, err := r.groups.GetAccountCount(ctx, groupID); err == nil {
		status.Total = int(total)
	}

	candidates, err := r.groups.ListAntiDegradePresetCandidates(ctx, groupID, preset)
	if err != nil {
		logger.LegacyPrintf("service.group_preset", "[GroupPreset] list candidates failed: group=%d err=%v", groupID, err)
		status.Failed++
		status.Failures = append(status.Failures, GroupPresetSyncFailure{Reason: err.Error()})
		return status
	}
	status.Candidates = len(candidates)

	applyCtx := ctx
	if preset != "" {
		applyCtx = WithAntiDegradeApplySource(ctx, AntiDegradeApplySource{
			Kind:    AntiDegradeSourceGroup,
			GroupID: groupID,
		})
	}

	limit := r.maxPer
	for _, accountID := range candidates {
		if ctx.Err() != nil {
			return status
		}
		if limit <= 0 {
			// 本轮额度用尽，剩余候选留给下一轮（已对齐的不会再进候选）。
			break
		}
		limit--

		var opErr error
		if preset != "" {
			_, opErr = r.anti.ApplyAntiDegradeMode(applyCtx, accountID, AntiDegradeMode(preset))
		} else {
			_, opErr = r.anti.RevertAntiDegrade(applyCtx, accountID)
		}
		if opErr == nil {
			if preset != "" {
				status.Applied++
			} else {
				status.Reverted++
			}
			continue
		}

		// 不适用（如 Anthropic 账号配 mode1）记为跳过而非失败，否则状态面板会长期飘红。
		if isGroupPresetSkipReason(opErr) {
			status.Skipped++
			continue
		}
		status.Failed++
		if len(status.Failures) < 20 {
			status.Failures = append(status.Failures, GroupPresetSyncFailure{
				AccountID: accountID,
				Reason:    infraerrors.Message(opErr),
			})
		}
		if status.Failed >= defaultGroupPresetMaxFailures {
			logger.LegacyPrintf("service.group_preset", "[GroupPreset] abort pass: group=%d failures=%d", groupID, status.Failed)
			break
		}
	}
	return status
}

// isGroupPresetSkipReason 判断错误属于"该账号本就不适用该策略"，应记为跳过。
func isGroupPresetSkipReason(err error) bool {
	if err == nil {
		return false
	}
	switch infraerrors.Reason(err) {
	case "PROTECTION_NOT_ELIGIBLE", "MODE1_NOT_ELIGIBLE", "INVALID_ANTI_DEGRADE_PRESET":
		return true
	}
	msg := strings.ToLower(infraerrors.Message(err))
	return strings.Contains(msg, "not eligible") || strings.Contains(msg, "不适用")
}

// GroupStatus 返回某分组的最近一轮收敛状态（不存在时返回 nil）。
func (r *GroupAntiDegradeReconciler) GroupStatus(groupID int64) *GroupPresetSyncStatus {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.status[groupID]
	if !ok || st == nil {
		return nil
	}
	copied := *st
	copied.Failures = append([]GroupPresetSyncFailure(nil), st.Failures...)
	return &copied
}

// Snapshot 返回所有有状态记录的分组（按 group_id 升序）。
func (r *GroupAntiDegradeReconciler) Snapshot() []GroupPresetSyncStatus {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]GroupPresetSyncStatus, 0, len(r.status))
	for _, st := range r.status {
		if st == nil {
			continue
		}
		copied := *st
		copied.Failures = append([]GroupPresetSyncFailure(nil), st.Failures...)
		out = append(out, copied)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}
