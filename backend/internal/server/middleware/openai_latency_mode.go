package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpenAILatencyModeSnapshot binds one immutable runtime setting snapshot to
// the request so every OpenAI hot-path decision observes the same mode.
func OpenAILatencyModeSnapshot(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil {
			mode := config.OpenAILatencyModeCompatible
			if settingService != nil {
				mode = settingService.GetOpenAILatencyMode()
			}
			ctx := service.WithOpenAILatencyMode(c.Request.Context(), mode)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}
