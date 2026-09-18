package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterDegradationAdminRoutes 注册降智检测的管理面路由。
//
// 与其它二开路由同组挂载，沿用管理员鉴权、限流与审计中间件。
func RegisterDegradationAdminRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	detector := admin.Group("/degradation-detection")
	{
		detector.GET("/groups", h.Admin.Degradation.Groups)
		detector.PUT("/groups/:id", h.Admin.Degradation.UpdateGroup)
		detector.GET("/overview", h.Admin.Degradation.Overview)
		detector.POST("/run", h.Admin.Degradation.Run)
		detector.POST("/stats/reset", h.Admin.Degradation.ResetPublicStats)
		// The public page is a curated surface: an operator lists what it renders
		// and removes anything that should not be published (a stray test image,
		// for example). Only artwork rows can be deleted.
		detector.GET("/works", h.Admin.Degradation.Works)
		detector.DELETE("/works/:id", h.Admin.Degradation.DeleteWork)
		detector.POST("/works/purge", h.Admin.Degradation.PurgeWorks)
	}
}

// RegisterDegradationPublicRoutes 注册公开展示页 /jiangzhijiance 的数据接口。
//
// 匿名可读：只暴露已净化的 SVG 与展示用元数据（模型、思考强度、耗时、时间），
// 不含账号凭据、提示词、原始答复或错误。因此只挂 PublicIP 限流，不挂 JWT，
// 也不套 BackendModeUserGuard——这是产品要求的公开页，backend 模式下同样对
// 匿名访客开放（前端路由白名单同步放行 /jiangzhijiance）。
func RegisterDegradationPublicRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	public := v1.Group("/jiangzhijiance")
	public.Use(panelRateLimiter.PublicIP())
	{
		public.GET("", h.DegradationPublic.Page)
		public.GET("/timeline", h.DegradationPublic.Timeline)
		public.GET("/records/:id/image", h.DegradationPublic.Image)
		public.GET("/records/:id/animation", h.DegradationPublic.Animation)
	}
}
