package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CodexTurnStateHandler exposes the turn-state pool to the admin panel.
//
// The raw token is never returned: the overview and the history carry only a
// fingerprint, a length and a countdown, which is everything an operator needs
// to see that the pool is healthy without turning the panel into a credential
// dump.
type CodexTurnStateHandler struct {
	svc *service.CodexTurnStateService
}

func NewCodexTurnStateHandler(svc *service.CodexTurnStateService) *CodexTurnStateHandler {
	return &CodexTurnStateHandler{svc: svc}
}

// Overview returns the configuration, the runtime counters and one row per
// enrolled account.
func (h *CodexTurnStateHandler) Overview(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "292 状态服务未启用")
		return
	}
	c.Header("Cache-Control", "no-store")
	overview, err := h.svc.Overview(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取 292 状态概览失败")
		return
	}
	response.Success(c, overview)
}

// UpdateConfig replaces the global configuration.
func (h *CodexTurnStateHandler) UpdateConfig(c *gin.Context) {
	actor, ok := intelligentAdminActor(c)
	if !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "292 状态服务未启用")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)

	var payload service.CodexTurnStateConfig
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		response.BadRequest(c, "配置格式不正确")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		response.BadRequest(c, "配置格式不正确")
		return
	}

	saved, err := h.svc.UpdateConfig(c.Request.Context(), actor, payload)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCodexTurnStateNoAccounts):
			response.BadRequest(c, "启用后必须至少选择一个检测分组或指定账号")
		case errors.Is(err, service.ErrCodexTurnStateDisabled):
			response.BadRequest(c, err.Error())
		default:
			response.BadRequest(c, err.Error())
		}
		return
	}
	response.Success(c, gin.H{"config": saved})
}

// Collect queues an immediate collection. account_id 0 means "every due account".
func (h *CodexTurnStateHandler) Collect(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "292 状态服务未启用")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var payload struct {
		AccountID int64 `json:"account_id"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&payload); err != nil || payload.AccountID < 0 {
		response.BadRequest(c, "账号参数不正确")
		return
	}
	queued, err := h.svc.CollectNow(c.Request.Context(), payload.AccountID)
	if err != nil {
		if errors.Is(err, service.ErrCodexTurnStateDisabled) {
			response.BadRequest(c, "请先启用 292 状态注入")
			return
		}
		response.InternalError(c, "触发采集失败")
		return
	}
	response.Success(c, gin.H{"queued": queued})
}

// Invalidate drops one account's state so the next request collects a new one.
func (h *CodexTurnStateHandler) Invalidate(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "292 状态服务未启用")
		return
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "账号 ID 不正确")
		return
	}
	if err := h.svc.Invalidate(c.Request.Context(), accountID); err != nil {
		response.InternalError(c, "作废状态失败")
		return
	}
	response.Success(c, gin.H{"account_id": accountID})
}

// History lists the observation timeline.
func (h *CodexTurnStateHandler) History(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "292 状态服务未启用")
		return
	}
	c.Header("Cache-Control", "no-store")
	page := parsePositiveInt(c.DefaultQuery("page", "1"), 1)
	pageSize := parsePositiveInt(c.DefaultQuery("page_size", "20"), 20)
	accountID, _ := strconv.ParseInt(strings.TrimSpace(c.DefaultQuery("account_id", "0")), 10, 64)
	if accountID < 0 {
		accountID = 0
	}
	history, err := h.svc.History(c.Request.Context(), accountID, page, pageSize)
	if err != nil {
		response.InternalError(c, "读取采集历史失败")
		return
	}
	response.Success(c, history)
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func (h *CodexTurnStateHandler) ClearHistory(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	if h == nil || h.svc == nil {
		response.InternalError(c, "状态池服务未启用")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var payload struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&payload); err != nil || !payload.Confirm {
		response.BadRequest(c, "请确认清空历史；当前锁定状态与采集次数会保留")
		return
	}
	deleted, kept, err := h.svc.ClearHistory(c.Request.Context())
	if err != nil {
		response.InternalError(c, "清空采集历史失败，请稍后重试")
		return
	}
	response.Success(c, gin.H{"deleted": deleted, "retained": kept})
}
