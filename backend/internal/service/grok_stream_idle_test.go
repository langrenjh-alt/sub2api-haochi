//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveGrokStreamIdleTimeout(t *testing.T) {
	require.Equal(t, 90*time.Second, resolveGrokStreamIdleTimeout(90))
	require.Equal(t, defaultGrokStreamIdleTimeout, resolveGrokStreamIdleTimeout(0))
	require.Equal(t, defaultGrokStreamIdleTimeout, resolveGrokStreamIdleTimeout(-1))
}

func TestGrokStreamIdleFailoverError(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}
	err := grokStreamIdleFailoverError(account, 180*time.Second)
	require.NotNil(t, err)
	require.Equal(t, 502, err.StatusCode)
	require.True(t, err.SafeToFailoverAfterWrite)
	require.True(t, err.RetryableOnSameAccount)
	require.True(t, err.RequestScopedTransient)
	require.Equal(t, 1, err.SameAccountRetryMax)
	require.Contains(t, string(err.ResponseBody), "empty_upstream")
	require.WithinDuration(t, time.Now().Add(180*time.Second), err.SameAccountRetryDeadline, 2*time.Second)
}

func TestGrokStreamIdleFailoverErrorRequiresGrokAccount(t *testing.T) {
	openAI := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	err := grokStreamIdleFailoverError(openAI, time.Second)
	require.False(t, err.RetryableOnSameAccount)
	require.True(t, err.RequestScopedTransient)
}

func TestSkipGrokPoolTempUnsched(t *testing.T) {
	require.False(t, skipGrokPoolTempUnsched(nil))
	require.False(t, skipGrokPoolTempUnsched(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}))
	require.False(t, skipGrokPoolTempUnsched(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}))
	require.True(t, skipGrokPoolTempUnsched(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}))
	require.False(t, skipGrokPoolTempUnsched(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}))
}

func TestTempUnscheduleGrok_PoolModeNeverWritesIdleTimeout(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       9001,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	svc.tempUnscheduleGrok(context.Background(), account, grokStreamIdleCooldown, "grok stream idle timeout")

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Nil(t, account.TempUnschedulableUntil)
	require.Empty(t, account.TempUnschedulableReason)
}

func TestTempUnscheduleGrok_NonPoolWritesIdleTimeout(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9002, Platform: PlatformGrok, Type: AccountTypeAPIKey}
	before := time.Now()

	svc.tempUnscheduleGrok(context.Background(), account, grokStreamIdleCooldown, "grok stream idle timeout")

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, account.ID, repo.lastTempUnschedID)
	require.Equal(t, "grok stream idle timeout", repo.lastTempUnschedReason)
	require.WithinDuration(t, before.Add(grokStreamIdleCooldown), repo.lastTempUnschedUntil, time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestTryTempUnschedulable_GrokPoolModeSkipped(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{
		ID:       9003,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"denied"},
					"duration_minutes": float64(7),
				},
			},
		},
	}

	handled := svc.tryTempUnschedulable(
		context.Background(),
		account,
		http.StatusForbidden,
		[]byte(`{"error":{"message":"entitlement denied"}}`),
	)

	require.False(t, handled)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestTriggerStreamTimeoutTempUnsched_GrokPoolModeSkipped(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{
		ID:       9004,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
	settings := &StreamTimeoutSettings{TempUnschedMinutes: 5}

	ok := svc.triggerStreamTimeoutTempUnsched(context.Background(), account, settings, "grok-4")

	require.False(t, ok)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestTempUnscheduleOpenAITransportError_GrokPoolModeSkipped(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       9005,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	svc.tempUnscheduleOpenAITransportError(context.Background(), account, "proxy refused")

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
