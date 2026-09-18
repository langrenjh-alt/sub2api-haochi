package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// Only mounted inside existing administrator authentication and compliance guards.
func registerPoolRunwayRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	admin.GET("/pool-runway", h.Admin.PoolRunway.Overview)
	admin.GET("/pool-runway/config", h.Admin.PoolRunway.Config)
	admin.PUT("/pool-runway/config", h.Admin.PoolRunway.Configure)
}
