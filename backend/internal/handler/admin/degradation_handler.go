package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DegradationHandler exposes the degradation detector to the group editor.
type DegradationHandler struct {
	svc *service.DegradationService
}

func NewDegradationHandler(svc *service.DegradationService) *DegradationHandler {
	return &DegradationHandler{svc: svc}
}

// Groups lists every group with its detector switch and parameters.
func (h *DegradationHandler) Groups(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	groups, err := h.svc.Groups(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"items": groups})
}

// UpdateGroup writes one group's detector configuration.
func (h *DegradationHandler) UpdateGroup(c *gin.Context) {
	actor, ok := intelligentAdminActor(c)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID < 1 {
		response.BadRequest(c, "invalid group identifier")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var req service.DegradationDetectionConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "无效的降智检测配置")
		return
	}
	updated, err := h.svc.UpdateGroupConfig(c.Request.Context(), actor, groupID, req)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"group_id": groupID, "config": updated})
}

// Overview returns the panel summary and the per-account detector state.
func (h *DegradationHandler) Overview(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	out, err := h.svc.Overview(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

// Run queues probes immediately for the given group (or every enabled group).
func (h *DegradationHandler) Run(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	var groupID int64
	if raw := c.Query("group_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			response.BadRequest(c, "invalid group_id")
			return
		}
		groupID = value
	}
	queued, err := h.svc.RunNow(c.Request.Context(), groupID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Accepted(c, gin.H{"queued": queued})
}

// Works lists the artworks the public page currently renders, so the operator
// can curate that page.
func (h *DegradationHandler) Works(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	page, size := 1, 24
	if raw := c.Query("page"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			page = value
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			size = value
		}
	}
	out, err := h.svc.Works(c.Request.Context(), page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

// DeleteWork removes one artwork from the public page.
func (h *DegradationHandler) DeleteWork(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "invalid artwork identifier")
		return
	}
	deleted, err := h.svc.DeleteWork(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	if !deleted {
		response.NotFound(c, "artwork not found")
		return
	}
	response.Success(c, gin.H{"deleted": 1})
}

// PurgeWorks empties the public page in one call.
func (h *DegradationHandler) PurgeWorks(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	removed, err := h.svc.PurgeWorks(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"deleted": removed})
}
