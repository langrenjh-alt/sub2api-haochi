package service

import (
	"context"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 分组统一防降智策略预设存放在 groups.anti_degrade_preset 列上，
// 通过后台收敛器对齐到组内账号；账号侧的并发/指纹字段仍是运行时唯一真相。

// AntiDegradePresetOff 是"关闭分组预设"的取值：不干预组内账号，各账号用自己的设置。
const AntiDegradePresetOff = ""

// 账号 anti_degrade 标记里的溯源键。分组预设关闭时据此只回滚"由分组写过"的账号，
// 不误伤管理员手工应用过同一策略的账号。
const (
	antiDegradeMarkerSourceKey       = "source"
	antiDegradeMarkerSourceGroupID   = "source_group_id"
	antiDegradeMarkerSourcePresetKey = "source_preset"
	// AntiDegradeSourceGroup 标记该次预设写入由分组收敛器发起。
	AntiDegradeSourceGroup = "group"
)

// AntiDegradeApplySource 描述一次预设写入的发起方。
type AntiDegradeApplySource struct {
	Kind    string
	GroupID int64
}

type antiDegradeApplySourceCtxKey struct{}

// WithAntiDegradeApplySource 让本次 ApplyAntiDegradeMode 在标记里落溯源信息。
func WithAntiDegradeApplySource(ctx context.Context, src AntiDegradeApplySource) context.Context {
	if src.Kind == "" {
		return ctx
	}
	return context.WithValue(ctx, antiDegradeApplySourceCtxKey{}, src)
}

func antiDegradeApplySourceFromContext(ctx context.Context) AntiDegradeApplySource {
	if ctx == nil {
		return AntiDegradeApplySource{}
	}
	src, _ := ctx.Value(antiDegradeApplySourceCtxKey{}).(AntiDegradeApplySource)
	return src
}

// AntiDegradeMarkerSource 读出账号标记里的溯源信息。
// 第二个返回值表示标记是否由指定分组写入。
func AntiDegradeMarkerSource(a *Account) (kind string, groupID int64, preset string, fromGroup bool) {
	if a == nil {
		return "", 0, "", false
	}
	marker, ok := a.Extra[AntiDegradeMarkerExtraKey].(map[string]any)
	if !ok {
		return "", 0, "", false
	}
	if v, ok := marker[antiDegradeMarkerSourceKey].(string); ok {
		kind = v
	}
	groupID = int64(mode1Int(marker[antiDegradeMarkerSourceGroupID]))
	if v, ok := marker[antiDegradeMarkerSourcePresetKey].(string); ok {
		preset = v
	}
	return kind, groupID, preset, kind == AntiDegradeSourceGroup
}

// NormalizeGroupAntiDegradePreset 校验并归一化分组级策略预设取值。
//
// 取值必须落在策略注册表里，或者为空字符串（关闭）。空串与 native_baseline 语义重合，
// 但后者在账号级是通过"还原快照"实现的（Apply 会显式拒绝它），放进分组会让收敛器
// 反复撞 400，所以这里直接拒绝，让管理员用"关闭"表达同样的意图。
func NormalizeGroupAntiDegradePreset(raw string) (string, error) {
	preset := strings.TrimSpace(raw)
	if preset == AntiDegradePresetOff {
		return AntiDegradePresetOff, nil
	}

	profile := antiDegradeStrategyProfile(AntiDegradeMode(preset))
	if profile.ID == "" {
		return "", infraerrors.New(
			http.StatusBadRequest,
			"INVALID_ANTI_DEGRADE_PRESET",
			"unknown anti-degrade preset: "+preset,
		)
	}
	if profile.ID == AntiDegradeModeNative {
		return "", infraerrors.New(
			http.StatusBadRequest,
			"INVALID_ANTI_DEGRADE_PRESET",
			"native baseline cannot be used as a group preset; clear the group preset instead",
		)
	}
	if !profile.ApplySupported {
		return "", infraerrors.New(
			http.StatusBadRequest,
			"INVALID_ANTI_DEGRADE_PRESET",
			"anti-degrade preset does not support being applied: "+preset,
		)
	}
	return string(profile.ID), nil
}

// IsGroupAntiDegradePresetActive 报告分组是否启用了统一预设。
func IsGroupAntiDegradePresetActive(preset string) bool {
	return strings.TrimSpace(preset) != AntiDegradePresetOff
}
