package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAILatencyModeSnapshotBindsRuntimeMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAILatencyMode: config.OpenAILatencyModeLowLatency}}
	settingService := service.NewSettingService(nil, cfg)
	router := gin.New()
	router.Use(OpenAILatencyModeSnapshot(settingService))
	router.GET("/", func(c *gin.Context) {
		mode, ok := service.OpenAILatencyModeFromContext(c.Request.Context())
		require.True(t, ok)
		c.String(http.StatusOK, mode)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, config.OpenAILatencyModeLowLatency, recorder.Body.String())
}

func TestOpenAILatencyModeSnapshotNilServiceUsesCompatibleMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(OpenAILatencyModeSnapshot(nil))
	router.GET("/", func(c *gin.Context) {
		mode, ok := service.OpenAILatencyModeFromContext(c.Request.Context())
		require.True(t, ok)
		c.String(http.StatusOK, mode)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, config.OpenAILatencyModeCompatible, recorder.Body.String())
}
