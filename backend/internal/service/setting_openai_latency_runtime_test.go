package service

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type openAILatencySettingRepoStub struct {
	mu      sync.Mutex
	values  map[string]string
	getErr  error
	setErr  error
	updates int
}

func (s *openAILatencySettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *openAILatencySettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *openAILatencySettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *openAILatencySettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *openAILatencySettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	for key, value := range settings {
		s.values[key] = value
	}
	s.updates++
	return nil
}

func (s *openAILatencySettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAILatencySettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestLoadOpenAILatencyModeSetting(t *testing.T) {
	t.Run("config default when row is missing", func(t *testing.T) {
		cfg := &config.Config{Gateway: config.GatewayConfig{OpenAILatencyMode: config.OpenAILatencyModeLowLatency}}
		svc := NewSettingService(&openAILatencySettingRepoStub{}, cfg)

		require.NoError(t, svc.LoadOpenAILatencyModeSetting(context.Background()))
		require.Equal(t, config.OpenAILatencyModeLowLatency, svc.GetOpenAILatencyMode())
	})

	t.Run("database overrides config", func(t *testing.T) {
		cfg := &config.Config{Gateway: config.GatewayConfig{OpenAILatencyMode: config.OpenAILatencyModeLowLatency}}
		repo := &openAILatencySettingRepoStub{values: map[string]string{
			SettingKeyOpenAILatencyMode: config.OpenAILatencyModeCompatible,
		}}
		svc := NewSettingService(repo, cfg)

		require.NoError(t, svc.LoadOpenAILatencyModeSetting(context.Background()))
		require.Equal(t, config.OpenAILatencyModeCompatible, svc.GetOpenAILatencyMode())
	})

	t.Run("invalid persisted value keeps config default", func(t *testing.T) {
		cfg := &config.Config{Gateway: config.GatewayConfig{OpenAILatencyMode: config.OpenAILatencyModeLowLatency}}
		repo := &openAILatencySettingRepoStub{values: map[string]string{SettingKeyOpenAILatencyMode: "turbo"}}
		svc := NewSettingService(repo, cfg)

		require.Error(t, svc.LoadOpenAILatencyModeSetting(context.Background()))
		require.Equal(t, config.OpenAILatencyModeLowLatency, svc.GetOpenAILatencyMode())
	})
}

func TestUpdateSettingsRefreshesOpenAILatencyModeWithoutMutatingConfig(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAILatencyMode: config.OpenAILatencyModeCompatible}}
	repo := &openAILatencySettingRepoStub{}
	svc := NewSettingService(repo, cfg)

	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{
		OpenAILatencyMode: config.OpenAILatencyModeLowLatency,
	}))
	require.Equal(t, config.OpenAILatencyModeLowLatency, svc.GetOpenAILatencyMode())
	require.Equal(t, config.OpenAILatencyModeLowLatency, repo.values[SettingKeyOpenAILatencyMode])
	require.Equal(t, config.OpenAILatencyModeCompatible, cfg.Gateway.OpenAILatencyMode)

	err := svc.UpdateSettings(context.Background(), &SystemSettings{OpenAILatencyMode: "turbo"})
	require.Error(t, err)
	require.Equal(t, "INVALID_OPENAI_LATENCY_MODE", infraerrors.Reason(err))
	require.Equal(t, config.OpenAILatencyModeLowLatency, svc.GetOpenAILatencyMode())
}

func TestOpenAILatencyModeAtomicSnapshotConcurrentAccess(t *testing.T) {
	svc := NewSettingService(nil, &config.Config{})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				if (worker+i)%2 == 0 {
					svc.storeOpenAILatencyMode(config.OpenAILatencyModeCompatible)
				} else {
					svc.storeOpenAILatencyMode(config.OpenAILatencyModeLowLatency)
				}
				mode := svc.GetOpenAILatencyMode()
				if mode != config.OpenAILatencyModeCompatible && mode != config.OpenAILatencyModeLowLatency {
					t.Errorf("unexpected OpenAI latency mode %q", mode)
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestOpenAILatencyModeRequestSnapshotRemainsStable(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		OpenAILatencyMode:               config.OpenAILatencyModeCompatible,
		OpenAIMissingInstructionsPolicy: config.OpenAIMissingInstructionsPolicyCodex,
		OpenAIStreamFlushPreamble:       false,
	}}
	settingService := NewSettingService(nil, cfg)
	settingService.storeOpenAILatencyMode(config.OpenAILatencyModeLowLatency)
	svc := &OpenAIGatewayService{cfg: cfg, settingService: settingService}
	ctx := WithOpenAILatencyMode(context.Background(), settingService.GetOpenAILatencyMode())

	settingService.storeOpenAILatencyMode(config.OpenAILatencyModeCompatible)

	require.True(t, svc.openAILowLatencyMode(ctx))
	require.True(t, svc.shouldFlushOpenAIStreamPreamble(ctx))
	require.Empty(t, svc.openAIMissingInstructions(ctx, "gpt-5.4"))
	require.False(t, svc.openAILowLatencyMode(context.Background()))
}

func TestOpenAILatencyContextWorksWithoutServiceConfig(t *testing.T) {
	ctx := WithOpenAILatencyMode(context.Background(), config.OpenAILatencyModeLowLatency)
	svc := &OpenAIGatewayService{}

	require.True(t, svc.openAILowLatencyMode(ctx))
	require.True(t, svc.shouldFlushOpenAIStreamPreamble(ctx))
	require.Empty(t, svc.openAIMissingInstructions(ctx, "gpt-5.4"))
}
