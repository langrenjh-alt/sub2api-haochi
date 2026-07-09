//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayServiceTempUnscheduleRetryableErrorSkipsBadGateway(t *testing.T) {
	repo := &mockAccountRepoForPlatform{}
	svc := &GatewayService{accountRepo: repo}

	svc.TempUnscheduleRetryableError(context.Background(), 42, &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		RetryableOnSameAccount: true,
		ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
	})

	require.Zero(t, repo.setTempUnschedulableCalls)
}

func TestGatewayServiceTempUnscheduleRetryableErrorKeepsBadRequestCooldown(t *testing.T) {
	repo := &mockAccountRepoForPlatform{}
	svc := &GatewayService{accountRepo: repo}

	svc.TempUnscheduleRetryableError(context.Background(), 43, &UpstreamFailoverError{
		StatusCode:             http.StatusBadRequest,
		RetryableOnSameAccount: true,
	})

	require.Equal(t, 1, repo.setTempUnschedulableCalls)
	require.Equal(t, int64(43), repo.lastTempUnschedulableID)
	require.Contains(t, repo.lastTempUnschedulableReason, "invalid project resource name")
}
