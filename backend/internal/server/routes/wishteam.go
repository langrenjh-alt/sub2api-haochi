package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// Mounted exclusively below the existing admin auth, audit and compliance stack.
func registerWishTeamRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	g := admin.Group("/wishteam5x")
	g.GET("", h.Admin.WishTeam.Overview)
	g.PUT("/config", h.Admin.WishTeam.Configure)
	g.POST("/run", h.Admin.WishTeam.Run)
	g.GET("/runs/:id/items", h.Admin.WishTeam.Items)
	g.GET("/items/:id/archive", h.Admin.WishTeam.Archive)
}
