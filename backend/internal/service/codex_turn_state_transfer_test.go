package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTurnStateTransferQualification(t *testing.T) {
	now := time.Now()
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})
	for _, tc := range []struct {
		name           string
		blocks, target int
		model          string
		expires        time.Time
		revoked, want  bool
	}{
		{"personal", 10, 0, cfg.Model, now.Add(time.Hour), false, true},
		{"team", 12, 332, cfg.Model, now.Add(time.Hour), false, true},
		{"wrong_length", 12, 292, cfg.Model, now.Add(time.Hour), false, false},
		{"degraded", 13, 0, cfg.Model, now.Add(time.Hour), false, false},
		{"expired", 12, 332, cfg.Model, now, false, false},
		{"revoked", 12, 332, cfg.Model, now.Add(time.Hour), true, false},
		{"wrong_model", 12, 332, "another-model", now.Add(time.Hour), false, false},
		{"unknown_model", 12, 332, "", now.Add(time.Hour), false, false},
		{"renewal_still_valid", 12, 332, cfg.Model, now.Add(time.Second), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.TargetStateLength = tc.target
			require.Equal(t, tc.want, cfg.TicketQualified(synthTurnState(tc.blocks, now), tc.model, tc.expires, tc.revoked, now))
		})
	}
}

func TestTurnStateUnknownModelCannotBindOrSuppressRenewal(t *testing.T) {
	s, repo, now := newShapeTestService(t, CodexTurnStateConfig{})
	token := synthTurnState(12, now)
	s.persist(context.Background(), codexTurnStateEvent{accountID: 7, state: token, statusCode: 200})
	require.Empty(t, repo.snapshot(), "an unknown-model observation must not shadow a qualified pin")
	s.storeCache(7, cachedTurnState{state: token, expiresAt: now.Add(time.Hour), recordID: 1})
	require.True(t, s.needsRefresh(7, s.cfg, now))
	_, _, injected := s.InjectionHeader(7, s.cfg.Model)
	require.False(t, injected)
}

func TestTurnStateTransferConfigCompatibility(t *testing.T) {
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})
	require.False(t, cfg.TransferActive())
	require.Equal(t, "account", cfg.HarvestTransport)
	require.Equal(t, 300, cfg.CycleCooldownSeconds)
	cfg.Enabled = true
	cfg.InjectEnabled = true
	cfg.TransferEnabled = true
	cfg.TransferReadyGroupID = 1
	cfg.TransferRecoveryGroupID = 2
	require.NoError(t, ValidateCodexTurnStateConfig(cfg))
	cfg.TransferRecoveryGroupID = 1
	require.Error(t, ValidateCodexTurnStateConfig(cfg))
	cfg.TransferRecoveryGroupID = 2
	cfg.HarvestTransport = "independent"
	require.Error(t, ValidateCodexTurnStateConfig(cfg))
	cfg.ProxyID = 3
	require.NoError(t, ValidateCodexTurnStateConfig(cfg))
	cfg.InjectEnabled = false
	require.False(t, cfg.TransferActive())
}

type transferReadFailure struct {
	*fakeTurnStateRepo
	moves int
}

func (r *transferReadFailure) TransferMembers(context.Context, CodexTurnStateConfig) (map[int64][]int64, error) {
	return map[int64][]int64{1: {10}}, nil
}
func (r *transferReadFailure) LatestPerAccount(context.Context, []int64) (map[int64]CodexTurnStateRecord, error) {
	return nil, errors.New("fixture read failure")
}
func (r *transferReadFailure) ReconcileTransfer(context.Context, CodexTurnStateConfig, int64, int64, bool) (*CodexTurnStateTransfer, error) {
	r.moves++
	return nil, nil
}
func (r *transferReadFailure) RecentTransfers(context.Context) ([]CodexTurnStateTransfer, error) {
	return nil, nil
}

func TestTurnStateTransferReadFailureDoesNotDemote(t *testing.T) {
	r := &transferReadFailure{fakeTurnStateRepo: newFakeTurnStateRepo()}
	s := NewCodexTurnStateService(r, nil, nil, nil)
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, InjectEnabled: true, TransferEnabled: true, TransferReadyGroupID: 10, TransferRecoveryGroupID: 20})
	s.cfg = cfg
	s.reconcileTransfers(context.Background(), cfg)
	require.Zero(t, r.moves)
}

type probeTransportSpy struct {
	calls    int
	upstream *captureUpstream
}

func (s *probeTransportSpy) DoOpenAIProbe(req *http.Request, p string, a *Account) (*http.Response, error) {
	s.calls++
	return s.upstream.Do(req, p, a.ID, a.Concurrency)
}

func TestTurnStateIndependentProbeBypassesPlugin(t *testing.T) {
	u := &captureUpstream{status: 200}
	a := codexOAuthAccount()
	s := probeService(u, a, CodexTurnStateConfig{})
	spy := &probeTransportSpy{upstream: u}
	s.SetTransport(spy)
	cfg := s.cfg
	cfg.ProxyID = 3
	cfg.HarvestTransport = "independent"
	result := s.probeCodexState(context.Background(), a, cfg, "http://fixture.invalid:8080")
	require.True(t, result.Attempted)
	require.Zero(t, spy.calls)
	require.True(t, u.lastRequest.Close)
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(u.lastRequest.Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(u.lastRequest.Context()))
	cfg.HarvestTransport = "account"
	s.probeCodexState(context.Background(), a, cfg, "http://fixture.invalid:8080")
	require.Equal(t, 1, spy.calls)
	require.False(t, u.lastRequest.Close)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(u.lastRequest.Context()))
	cfg.HarvestTransport = "independent"
	cfg.ProxyID = 0
	result = s.probeCodexState(context.Background(), a, cfg, "")
	require.False(t, result.Attempted)
	require.Equal(t, 1, spy.calls)
}

func TestTurnStateFinalAttemptRetainsBackoff(t *testing.T) {
	u := &speedUpstream{status: 500}
	s := speedService(t, u)
	s.repo.(*fakeTurnStateRepo).cfg.MaxAttemptsPerCycle = 1
	_, err := s.CollectNow(context.Background(), 1)
	require.NoError(t, err)
	s.tick(context.Background(), true)
	s.jobsMu.Lock()
	deadline := s.retryAfter[1]
	s.jobsMu.Unlock()
	require.Greater(t, time.Until(deadline), time.Second)
	_, err = s.CollectNow(context.Background(), 1)
	require.NoError(t, err)
	s.tick(context.Background(), true)
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Equal(t, 1, u.calls[1], "manual round reset must not bypass final-attempt backoff")
}

func TestTurnStateManualProbeWithValidPinStillRetainsRateLimit(t *testing.T) {
	u := &speedUpstream{status: http.StatusTooManyRequests}
	s := speedService(t, u)
	s.reloadConfig(context.Background())
	now := time.Now()
	s.storeCache(1, cachedTurnState{
		state: synthTurnState(12, now), model: s.cfg.Model,
		expiresAt: now.Add(time.Hour), issuedAt: now, recordID: 1,
	})
	_, err := s.CollectNow(context.Background(), 1)
	require.NoError(t, err)
	s.tick(context.Background(), true)
	require.False(t, s.needsRefresh(1, s.cfg, now), "failed manual probe must preserve the valid pin")
	s.jobsMu.Lock()
	deadline := s.retryAfter[1]
	s.jobsMu.Unlock()
	require.Greater(t, time.Until(deadline), 25*time.Second)
	_, err = s.CollectNow(context.Background(), 1)
	require.NoError(t, err)
	s.tick(context.Background(), true)
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Equal(t, 1, u.calls[1])
}
