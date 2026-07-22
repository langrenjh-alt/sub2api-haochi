package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokRefreshJitterIsStableAndBounded(t *testing.T) {
	const accountID = int64(30001)
	maxJitter := time.Hour

	first := grokRefreshJitterForAccount(accountID, maxJitter)
	second := grokRefreshJitterForAccount(accountID, maxJitter)
	require.Equal(t, first, second)
	require.GreaterOrEqual(t, first, time.Duration(0))
	require.LessOrEqual(t, first, maxJitter)
	require.NotEqual(t, first, grokRefreshJitterForAccount(accountID+1, maxJitter))
}

func TestGrokTokenRefresherNeedsRefreshUsesAccountJitter(t *testing.T) {
	const accountID = int64(60000)
	refresher := NewGrokTokenRefresher(nil, time.Hour)
	jitter := grokRefreshJitterForAccount(accountID, time.Hour)
	account := &Account{
		ID:       accountID,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
		},
	}

	account.Credentials["expires_at"] = time.Now().Add(grokTokenRefreshSkew + jitter + time.Minute).UTC().Format(time.RFC3339Nano)
	require.False(t, refresher.NeedsRefresh(account, 30*time.Minute))

	account.Credentials["expires_at"] = time.Now().Add(grokTokenRefreshSkew + jitter - time.Minute).UTC().Format(time.RFC3339Nano)
	require.True(t, refresher.NeedsRefresh(account, 30*time.Minute))
}

func TestGrokRefreshJitterIsClamped(t *testing.T) {
	refresher := NewGrokTokenRefresher(nil, 24*time.Hour)
	require.Equal(t, maxGrokTokenRefreshJitter, refresher.refreshJitter)
}
