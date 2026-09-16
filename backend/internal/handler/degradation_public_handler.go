package handler

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DegradationPublicHandler serves the anonymous /jiangzhijiance/ artwork feed.
//
// The payload is intentionally narrow: no account identity beyond an opaque id,
// no prompt, no raw model output, no error text. Images are re-encoded through
// the inert SVG preview pipeline before they leave the process.
type DegradationPublicHandler struct {
	svc *service.DegradationService
}

func NewDegradationPublicHandler(svc *service.DegradationService) *DegradationPublicHandler {
	return &DegradationPublicHandler{svc: svc}
}

func degradationPageParams(c *gin.Context) (int, int) {
	page, size := 1, 6
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
	return page, size
}

func (h *DegradationPublicHandler) Page(c *gin.Context) {
	page, size := degradationPageParams(c)
	out, err := h.svc.PublicPage(c.Request.Context(), page, size)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

// Timeline is the other half of the public page: it reports counts per bucket
// so a visitor can see whether the model was degraded during a window.
func (h *DegradationPublicHandler) Timeline(c *gin.Context) {
	hours := 24
	if raw := c.Query("hours"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			hours = value
		}
	}
	out, err := h.svc.Timeline(c.Request.Context(), hours)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

// Image streams one sanitized SVG with the same CSP sandbox the admin preview
// uses. The body is never treated as HTML.
func (h *DegradationPublicHandler) Image(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "invalid identifier")
		return
	}
	work, err := h.svc.PublicWork(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	if work.Image == "" {
		response.NotFound(c, "test image unavailable")
		return
	}
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'none'; sandbox")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("Content-Disposition", "inline; filename=degradation-preview.svg")
	c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", []byte(work.Image))
}
