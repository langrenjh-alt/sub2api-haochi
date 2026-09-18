package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// Only mounted inside the existing administrator authentication and compliance
// guards.
func registerCodexTurnStateRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	states := admin.Group("/codex-turn-state")
	{
		states.GET("", h.Admin.CodexTurnState.Overview)
		states.PUT("/config", h.Admin.CodexTurnState.UpdateConfig)
		states.POST("/collect", h.Admin.CodexTurnState.Collect)
		states.POST("/accounts/:id/invalidate", h.Admin.CodexTurnState.Invalidate)
		states.GET("/history", h.Admin.CodexTurnState.History)
		states.DELETE("/history", h.Admin.CodexTurnState.ClearHistory)
	}
}
