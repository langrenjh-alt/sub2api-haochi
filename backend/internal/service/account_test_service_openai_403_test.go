package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountTestOpenAI403Repo struct {
	AccountRepository
	account       *Account
	setErrorID    int64
	setErrorMsg   string
	setErrorCalls int
}

func (r *accountTestOpenAI403Repo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *accountTestOpenAI403Repo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func (r *accountTestOpenAI403Repo) SetError(_ context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.setErrorID = id
	r.setErrorMsg = errorMsg
	return nil
}

func TestAccountTestService_OpenAI403InactiveWorkspaceMemberMarksAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          401,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "workspace-id",
		},
	}
	repo := &accountTestOpenAI403Repo{account: account}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Personal access token owner is not an active member of the selected workspace.","type":null,"code":"biscuit_baker_service_auth_credential_error_status","param":null},"status":403}`,
		)),
	}}
	service := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/401/test", bytes.NewReader(nil))

	err := service.TestAccountConnection(c, account.ID, "gpt-5.4", "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "API returned 403")
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, account.ID, repo.setErrorID)
	require.Contains(t, repo.setErrorMsg, "Workspace membership invalid (403)")
	require.Contains(t, repo.setErrorMsg, "not an active member of the selected workspace")
}

func TestAccountTestService_OpenAI403GenericFailureDoesNotMutateAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          402,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "workspace-id",
		},
	}
	repo := &accountTestOpenAI403Repo{account: account}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporary edge rejection"}}`)),
	}}
	service := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/402/test", bytes.NewReader(nil))

	err := service.TestAccountConnection(c, account.ID, "gpt-5.4", "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "API returned 403")
	require.Zero(t, repo.setErrorCalls)
}
