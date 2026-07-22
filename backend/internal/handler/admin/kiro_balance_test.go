package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildKiroRSBalanceConfigRequiresKiroMarker(t *testing.T) {
	account := &service.Account{
		ID:       1,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":          "https://api.example.com",
			"kiro_rs_admin_key": "secret",
		},
	}

	_, ok := buildKiroRSBalanceConfig(account)
	require.False(t, ok)

	account.Credentials["kiro_rs_credential_id"] = "7"
	cfg, ok := buildKiroRSBalanceConfig(account)
	require.True(t, ok)
	require.Equal(t, "https://api.example.com", cfg.BaseURL)
	require.Equal(t, "7", cfg.CredentialID)
	require.Equal(t, "secret", cfg.AdminKey)
}

func TestFetchKiroRSBalanceParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/credentials/1/balance", r.URL.Path)
		require.Equal(t, "secret", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subscriptionTitle":"KIRO PRO","currentUsage":0.07,"usageLimit":1000,"remaining":999.93,"usagePercentage":0.007}`))
	}))
	defer server.Close()

	kiroRSBalanceCache.Clear()
	info := fetchKiroRSBalance(context.Background(), kiroRSBalanceConfig{
		BaseURL:      server.URL,
		AdminKey:     "secret",
		CredentialID: "1",
	})

	require.Empty(t, info.Error)
	require.Equal(t, "KIRO PRO", info.SubscriptionTitle)
	require.Equal(t, 0.07, info.CurrentUsage)
	require.Equal(t, 1000.0, info.UsageLimit)
	require.Equal(t, 999.93, info.Remaining)
	require.Equal(t, 0.007, info.UsagePercentage)
}
