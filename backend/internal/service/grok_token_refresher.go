package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

const grokTokenRefreshSkew = time.Hour

const maxGrokTokenRefreshJitter = 2 * time.Hour

type GrokTokenRefresher struct {
	grokOAuthService GrokOAuthTokenService
	refreshJitter    time.Duration
}

func NewGrokTokenRefresher(grokOAuthService GrokOAuthTokenService, refreshJitter ...time.Duration) *GrokTokenRefresher {
	jitter := time.Duration(0)
	if len(refreshJitter) > 0 && refreshJitter[0] > 0 {
		jitter = min(refreshJitter[0], maxGrokTokenRefreshJitter)
	}
	return &GrokTokenRefresher{grokOAuthService: grokOAuthService, refreshJitter: jitter}
}

func (r *GrokTokenRefresher) CacheKey(account *Account) string {
	return GrokTokenCacheKey(account)
}

func (r *GrokTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.Platform == PlatformGrok && account.Type == AccountTypeOAuth &&
		strings.TrimSpace(account.GetGrokRefreshToken()) != ""
}

func (r *GrokTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil || strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return false
	}
	if strings.TrimSpace(account.GetGrokAccessToken()) == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	if refreshWindow < grokTokenRefreshSkew {
		refreshWindow = grokTokenRefreshSkew
	}
	refreshWindow += grokRefreshJitterForAccount(account.ID, r.refreshJitter)
	return time.Until(*expiresAt) < refreshWindow
}

// grokRefreshJitterForAccount spreads synchronized account expirations across a
// stable window. The same account keeps the same offset across processes and
// restarts, so leader changes cannot move it back and forth between windows.
func grokRefreshJitterForAccount(accountID int64, maxJitter time.Duration) time.Duration {
	if accountID <= 0 || maxJitter <= 0 {
		return 0
	}
	maxJitter = min(maxJitter, maxGrokTokenRefreshJitter)

	// SplitMix64 provides a cheap, deterministic avalanche for sequential IDs.
	x := uint64(accountID) + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return time.Duration(x % (uint64(maxJitter) + 1))
}

func (r *GrokTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.grokOAuthService == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	tokenInfo, err := r.grokOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	newCredentials := r.grokOAuthService.BuildAccountCredentials(tokenInfo)
	newCredentials = MergeCredentials(account.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(account.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	return newCredentials, nil
}
