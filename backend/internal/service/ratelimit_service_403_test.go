//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type runtimeBlockRecorder struct {
	accounts   []*Account
	until      []time.Time
	reasons    []string
	clearedIDs []int64
}

func (r *runtimeBlockRecorder) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	r.accounts = append(r.accounts, account)
	r.until = append(r.until, until)
	r.reasons = append(r.reasons, reason)
}

func (r *runtimeBlockRecorder) ClearAccountSchedulingBlock(accountID int64) {
	r.clearedIDs = append(r.clearedIDs, accountID)
}

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitRetriesWithoutTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountRuntimeBlocker(blocker)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"temporary edge rejection"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Empty(t, repo.lastTempReason)
	require.Empty(t, blocker.accounts)
	require.Empty(t, blocker.reasons)
	require.Empty(t, blocker.until)
}

func TestRateLimitService_HandleUpstreamError_OpenAI403ThresholdDoesNotDisable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       302,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"workspace forbidden by policy"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Empty(t, repo.lastErrorMsg)
}

func TestRateLimitService_HandleUpstreamError_OpenAI403InactiveWorkspaceMemberDisablesAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountRuntimeBlocker(blocker)
	account := &Account{
		ID:       303,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"selected workspace"},
					"duration_minutes": float64(10),
				},
			},
		},
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"Personal access token owner is not an active member of the selected workspace.","type":null,"code":"biscuit_baker_service_auth_credential_error_status","param":null},"status":403}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, account.ID, repo.lastErrorID)
	require.Contains(t, repo.lastErrorMsg, "Workspace membership invalid (403)")
	require.Contains(t, repo.lastErrorMsg, "not an active member of the selected workspace")
	require.Equal(t, 0, repo.tempCalls)
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, account.ID, blocker.accounts[0].ID)
	require.Equal(t, "auth_error", blocker.reasons[0])
}

func TestRateLimitService_HandleUpstreamError_OpenAI403InactiveWorkspaceMemberDisablesCredentialOwner(t *testing.T) {
	const parentID = int64(304)
	repo := &rateLimitAccountRepoStub{}
	repo.accountsByID = map[int64]*Account{
		parentID: {
			ID:       parentID,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
		},
	}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountRuntimeBlocker(blocker)
	shadowParentID := parentID
	shadow := &Account{
		ID:              305,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		ParentAccountID: &shadowParentID,
		QuotaDimension:  QuotaDimensionSpark,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		shadow,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"Personal access token owner is not an active member of the selected workspace.","code":"biscuit_baker_service_auth_credential_error_status"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, parentID, repo.lastErrorID)
	require.Equal(t, 0, repo.tempCalls)
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, parentID, blocker.accounts[0].ID)
}
