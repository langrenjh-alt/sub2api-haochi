package service

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const animationFixture = `<!doctype html><html><head><style>.bob{animation:bob 1s linear infinite}@keyframes bob{from{transform:translateX(0)}to{transform:translateX(30px)}}</style></head><body><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 100"><g class="bob"><circle cx="50" cy="50" r="20" fill="red"><animate attributeName="cx" values="50;120;50" dur="2s" repeatCount="indefinite"/></circle></g></svg></body></html>`

func TestDegradationAnimationPreservesMotionButThumbnailRemainsStatic(t *testing.T) {
	animated, err := PrepareDegradationAnimation(animationFixture)
	require.NoError(t, err)
	require.True(t, animated.Animated)
	require.Contains(t, animated.Document, "@keyframes")
	require.Contains(t, animated.Document, "<animate")
	require.Contains(t, animated.Document, `class="bob"`)
	require.Contains(t, animated.Document, "script-src 'none'")
	thumbnail, _, err := PrepareIntelligentSVGPreview(animationFixture)
	require.NoError(t, err)
	require.NotContains(t, thumbnail, "<animate")
	require.NotContains(t, thumbnail, "@keyframes")
	if dir := os.Getenv("DEGRADATION_ANIMATION_FIXTURE_DIR"); dir != "" {
		require.NoError(t, os.WriteFile(dir+"/animated.html", []byte(animated.Document), 0600))
		require.NoError(t, os.WriteFile(dir+"/thumbnail.svg", []byte(thumbnail), 0600))
	}
}

func TestDegradationAnimationRejectsExecutableOrExternalContent(t *testing.T) {
	payload := `<html><head><style>@import "https://bad.invalid/x"; .x{fill:red}</style></head><body><svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)">
<script>alert(1)</script><foreignObject><iframe src="https://bad.invalid"/></foreignObject>
<a href="https://bad.invalid"><text>link</text></a><image href="https://bad.invalid/x"/>
<use href="https://bad.invalid/ref"/><circle r="20" onclick="alert(2)" style="fill:url(https://bad.invalid/x)"/>
<animate attributeName="href" values="javascript:alert(1)"/><animate attributeName="onclick" values="alert(1)"/>
<animate attributeName="opacity" values="0;1" dur="1s"/>
</svg></body></html>`
	result, err := PrepareDegradationAnimation(payload)
	require.NoError(t, err)
	for _, forbidden := range []string{"alert(", "bad.invalid", "<script", "<iframe", "<foreignObject", "<image", "onclick=", "onload=", `attributeName="href"`} {
		require.NotContains(t, result.Document, forbidden)
	}
	require.Contains(t, result.Document, `attributeName="opacity"`)
	require.Contains(t, result.Document, "connect-src 'none'")
	require.Contains(t, result.Document, "form-action 'none'")
}

func TestDegradationAnimationLegacyStaticAndInvalidSource(t *testing.T) {
	result, err := PrepareDegradationAnimation(`<svg><circle r="10"/></svg>`)
	require.NoError(t, err)
	require.False(t, result.Animated)
	for _, source := range []string{"no svg", "<svg><broken></svg>", strings.Repeat("x", 513<<10), `<svg><?evil data?><circle r="1"/></svg>`} {
		_, err := PrepareDegradationAnimation(source)
		require.Error(t, err)
	}
}
