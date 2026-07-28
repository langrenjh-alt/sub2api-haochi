package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	sseContentType          = "text/event-stream"
	sseCacheControl         = "no-cache, no-transform"
	xAccelBufferingHeader   = "X-Accel-Buffering"
	xAccelBufferingDisabled = "no"
)

type streamingHeadersResponseWriter struct {
	gin.ResponseWriter
}

func (w *streamingHeadersResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *streamingHeadersResponseWriter) prepareStreamingHeaders() {
	if w == nil || w.ResponseWriter == nil {
		return
	}
	header := w.ResponseWriter.Header()
	contentType := strings.ToLower(strings.TrimSpace(header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, sseContentType) {
		return
	}
	header.Set(xAccelBufferingHeader, xAccelBufferingDisabled)
	header.Set("Cache-Control", sseCacheControl)
}

func (w *streamingHeadersResponseWriter) WriteHeader(statusCode int) {
	w.prepareStreamingHeaders()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *streamingHeadersResponseWriter) WriteHeaderNow() {
	w.prepareStreamingHeaders()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *streamingHeadersResponseWriter) Write(data []byte) (int, error) {
	w.prepareStreamingHeaders()
	return w.ResponseWriter.Write(data)
}

func (w *streamingHeadersResponseWriter) WriteString(data string) (int, error) {
	w.prepareStreamingHeaders()
	return w.ResponseWriter.WriteString(data)
}

func (w *streamingHeadersResponseWriter) Flush() {
	w.prepareStreamingHeaders()
	w.ResponseWriter.Flush()
}

// StreamingResponseHeaders prevents reverse proxies from buffering or
// transforming SSE responses, including handlers that omit proxy hints.
func StreamingResponseHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		writer := &streamingHeadersResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
	}
}
