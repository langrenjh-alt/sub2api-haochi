package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runStreamingHeadersRequest(t *testing.T, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(StreamingResponseHeaders())
	router.GET("/stream", handler)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stream", nil))
	return recorder
}

func TestStreamingResponseHeadersAppliedBeforeFlush(t *testing.T) {
	recorder := runStreamingHeadersRequest(t, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "public, max-age=60")
		_, err := c.Writer.WriteString("data: first\n\n")
		require.NoError(t, err)
		c.Writer.Flush()
	})

	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.Equal(t, "no-cache, no-transform", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "data: first\n\n", recorder.Body.String())
}

func TestStreamingResponseHeadersAppliedOnExplicitWriteHeader(t *testing.T) {
	recorder := runStreamingHeadersRequest(t, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusAccepted)
		c.Writer.WriteHeaderNow()
	})

	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.Equal(t, "no-cache, no-transform", recorder.Header().Get("Cache-Control"))
}

func TestStreamingResponseHeadersLeavesJSONUnchanged(t *testing.T) {
	recorder := runStreamingHeadersRequest(t, func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=60")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	require.Empty(t, recorder.Header().Get("X-Accel-Buffering"))
	require.Equal(t, "public, max-age=60", recorder.Header().Get("Cache-Control"))
}

func TestStreamingHeadersResponseWriterUnwraps(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	baseWriter := c.Writer
	writer := &streamingHeadersResponseWriter{ResponseWriter: baseWriter}
	require.Same(t, baseWriter, writer.Unwrap())
}
