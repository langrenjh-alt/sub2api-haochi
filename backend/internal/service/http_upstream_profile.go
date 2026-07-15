package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// HTTPUpstreamProfile marks HTTP upstream requests that need provider-specific
// transport policy.
type HTTPUpstreamProfile string

const (
	HTTPUpstreamProfileDefault HTTPUpstreamProfile = ""
	HTTPUpstreamProfileOpenAI  HTTPUpstreamProfile = "openai"
)

type httpUpstreamProfileContextKey struct{}
type openAILatencyModeContextKey struct{}

// WithHTTPUpstreamProfile injects an upstream transport profile into ctx.
func WithHTTPUpstreamProfile(ctx context.Context, profile HTTPUpstreamProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile == HTTPUpstreamProfileDefault {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamProfileContextKey{}, profile)
}

// HTTPUpstreamProfileFromContext resolves the upstream transport profile from ctx.
func HTTPUpstreamProfileFromContext(ctx context.Context) HTTPUpstreamProfile {
	if ctx == nil {
		return HTTPUpstreamProfileDefault
	}
	profile, ok := ctx.Value(httpUpstreamProfileContextKey{}).(HTTPUpstreamProfile)
	if !ok {
		return HTTPUpstreamProfileDefault
	}
	switch profile {
	case HTTPUpstreamProfileOpenAI:
		return profile
	default:
		return HTTPUpstreamProfileDefault
	}
}

// WithOpenAILatencyMode binds one immutable mode snapshot to a request.
func WithOpenAILatencyMode(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != config.OpenAILatencyModeLowLatency {
		mode = config.OpenAILatencyModeCompatible
	}
	return context.WithValue(ctx, openAILatencyModeContextKey{}, mode)
}

// OpenAILatencyModeFromContext returns the request-scoped mode snapshot.
func OpenAILatencyModeFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	mode, ok := ctx.Value(openAILatencyModeContextKey{}).(string)
	if !ok {
		return "", false
	}
	switch mode {
	case config.OpenAILatencyModeCompatible, config.OpenAILatencyModeLowLatency:
		return mode, true
	default:
		return "", false
	}
}
