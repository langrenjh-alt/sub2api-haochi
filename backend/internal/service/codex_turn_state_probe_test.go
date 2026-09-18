package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// captureUpstream is an HTTPUpstream that records the request it was handed and
// replies with a canned response. It is how these tests assert on the wire shape
// the collector actually produces, without touching the network.
type captureUpstream struct {
	lastRequest *http.Request
	lastProxy   string
	lastAccount int64
	lastBody    []byte

	status  int
	headers map[string]string
	body    string
}

func (c *captureUpstream) Do(req *http.Request, proxyURL string, accountID int64, _ int) (*http.Response, error) {
	c.lastRequest = req
	c.lastProxy = proxyURL
	c.lastAccount = accountID
	if req.Body != nil {
		c.lastBody, _ = io.ReadAll(req.Body)
	}
	header := http.Header{}
	for key, value := range c.headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: c.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}, nil
}

func (c *captureUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return c.Do(req, proxyURL, accountID, concurrency)
}

// stubProxyRepo serves one dynamic proxy and leaves the rest of the (large)
// interface unimplemented: the collector only ever calls GetByID.
type stubProxyRepo struct {
	ProxyRepository
	dynamic *Proxy
}

func (r *stubProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if r.dynamic == nil || r.dynamic.ID != id {
		return nil, ErrProxyNotFound
	}
	return r.dynamic, nil
}

// staticAccountRepo serves one account and treats it as its own credential owner.
type staticAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *staticAccountRepo) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }
func (r *staticAccountRepo) GetByIDs(context.Context, []int64) ([]*Account, error) {
	if r.account == nil {
		return nil, nil
	}
	return []*Account{r.account}, nil
}

func codexOAuthAccount() *Account {
	return &Account{
		ID:          4242,
		Name:        "plus-account",
		Platform:    PlatformOpenAI,
		Type:        "oauth",
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":               "test-access-token",
			"chatgpt_account_id":         "acct-123",
			"chatgpt_account_is_fedramp": false,
		},
		Concurrency: 3,
	}
}

// probeService builds a service whose only dependency is the capturing upstream.
func probeService(upstream HTTPUpstream, account *Account, cfg CodexTurnStateConfig) *CodexTurnStateService {
	svc := NewCodexTurnStateService(newFakeTurnStateRepo(), &staticAccountRepo{account: account}, nil, upstream)
	svc.cfg = NormalizeCodexTurnStateConfig(cfg)
	svc.cfgAt = time.Now()
	return svc
}

func TestProbeReadsTheStateFromTheResponseHeader(t *testing.T) {
	upstream := &captureUpstream{
		status:  http.StatusOK,
		headers: map[string]string{"X-Codex-Turn-State": "gAAAAABharvested"},
		body:    "event: response.created\ndata: {}\n",
	}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, InjectEnabled: true, GroupIDs: []int64{24}})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "http://user:pass@proxy.example:3000")

	if result.Err != "" {
		t.Fatalf("probe error: %s", result.Err)
	}
	if result.State != "gAAAAABharvested" {
		t.Fatalf("state = %q, want the response header value", result.State)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Errorf("status = %d", result.HTTPStatus)
	}
	if upstream.lastProxy != "http://user:pass@proxy.example:3000" {
		t.Errorf("proxy = %q, want the configured dynamic proxy", upstream.lastProxy)
	}
}

func TestProbeSendsACodexShapedRequest(t *testing.T) {
	upstream := &captureUpstream{status: http.StatusOK, headers: map[string]string{"X-Codex-Turn-State": "state"}}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})

	if result := svc.probeCodexState(context.Background(), account, svc.cfg, ""); result.Err != "" {
		t.Fatalf("probe error: %s", result.Err)
	}

	req := upstream.lastRequest
	if req == nil {
		t.Fatal("no request was sent")
	}
	if req.URL.String() != chatgptCodexURL {
		t.Errorf("url = %s, want %s", req.URL, chatgptCodexURL)
	}
	if req.Host != "chatgpt.com" {
		t.Errorf("host = %q, want chatgpt.com", req.Host)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Errorf("authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-123" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	// The upstream classifies a Codex client by the engine fingerprint; without
	// these the probe is treated as a non-Codex caller.
	for _, header := range []string{"X-Codex-Window-ID", "Originator", "Version", "User-Agent", "Accept"} {
		if strings.TrimSpace(req.Header.Get(header)) == "" {
			t.Errorf("header %s is missing from the probe request", header)
		}
	}
	if upstream.lastAccount != account.ID {
		t.Errorf("account id = %d, want %d", upstream.lastAccount, account.ID)
	}

	var payload map[string]any
	if err := json.Unmarshal(upstream.lastBody, &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if payload["model"] == "" || payload["model"] == nil {
		t.Error("payload has no model")
	}
	if payload["store"] != false {
		t.Error("OAuth Codex requests must send store=false")
	}
	if _, ok := payload["client_metadata"].(map[string]any); !ok {
		t.Error("payload is missing client_metadata")
	}
}

func TestProbeTreatsARevocationStatusAsRevoked(t *testing.T) {
	// The revocation list is an operator knob rather than a built-in: "312" is a
	// token length, so it only revokes once someone names it as a status.
	upstream := &captureUpstream{status: CodexTurnStateDefaultRevocationStatus, body: `{"detail":"revoked"}`}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{
		Enabled:            true,
		GroupIDs:           []int64{24},
		RevocationStatuses: []int{CodexTurnStateDefaultRevocationStatus},
	})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "")
	if !result.Revoked {
		t.Fatalf("status %d must be treated as a revocation, got %+v", upstream.status, result)
	}
	if result.Err != "" {
		t.Errorf("a revocation is a state outcome, not an error: %s", result.Err)
	}
}

func TestProbeDoesNotRevokeOnTheWriteUpStatusByDefault(t *testing.T) {
	upstream := &captureUpstream{status: CodexTurnStateDefaultRevocationStatus, body: `{"detail":"no state here"}`}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "")
	if result.Revoked {
		t.Fatalf("status %d must not revoke anything without an explicit list, got %+v", upstream.status, result)
	}
}

func TestProbeReadsTheStateFromAStateBearingStatusBody(t *testing.T) {
	// The write-up describes the state arriving inside the body of a
	// non-standard success status. That shape must work too.
	upstream := &captureUpstream{
		status: CodexTurnStateDefaultStateStatus,
		body:   `{"current_turn_state":"gAAAAABfrombody"}`,
	}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "")
	if result.State != "gAAAAABfrombody" {
		t.Fatalf("state = %q, want the body field", result.State)
	}
	if result.Err != "" {
		t.Errorf("unexpected error: %s", result.Err)
	}
}

func TestProbeReportsAnUpstreamErrorWithoutAState(t *testing.T) {
	upstream := &captureUpstream{status: http.StatusForbidden, body: `<html>blocked</html>`}
	account := codexOAuthAccount()
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "")
	if result.Err == "" {
		t.Fatal("a 403 without a state must be reported as an error")
	}
	if !strings.Contains(result.Err, "403") {
		t.Errorf("error = %q, want it to mention the status", result.Err)
	}
}

func TestProbeRefusesAnAccountWithoutAToken(t *testing.T) {
	upstream := &captureUpstream{status: http.StatusOK}
	account := codexOAuthAccount()
	account.Credentials = map[string]any{"chatgpt_account_id": "acct-123"}
	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})

	result := svc.probeCodexState(context.Background(), account, svc.cfg, "")
	if !strings.Contains(result.Err, "access_token") {
		t.Fatalf("error = %q, want a missing-token report", result.Err)
	}
	if upstream.lastRequest != nil {
		t.Fatal("no request may be sent without a token")
	}
}

func TestProbeOneUsesTheConfiguredDynamicProxyOverTheAccountProxy(t *testing.T) {
	// resolveProxyURL is the decision point: the configured dynamic IP proxy is
	// what the collection path is for, so it must win over the account's own.
	upstream := &captureUpstream{status: http.StatusOK, headers: map[string]string{"X-Codex-Turn-State": "state"}}
	account := codexOAuthAccount()
	account.ProxyID = turnStateInt64Ptr(7)
	account.Proxy = &Proxy{ID: 7, Protocol: "http", Host: "account-proxy.example", Port: 8080}

	repo := newFakeTurnStateRepo()
	proxyRepo := &stubProxyRepo{dynamic: &Proxy{ID: 9, Protocol: "http", Host: "dynamic.example", Port: 3000, Username: "u", Password: "p"}}
	svc := NewCodexTurnStateService(repo, &staticAccountRepo{account: account}, proxyRepo, upstream)
	svc.cfg = NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}, ProxyID: 9})
	svc.cfgAt = time.Now()

	svc.probeOne(context.Background(), account.ID, svc.cfg)

	if !strings.Contains(upstream.lastProxy, "dynamic.example:3000") {
		t.Fatalf("proxy = %q, want the configured dynamic proxy", upstream.lastProxy)
	}
	if strings.Contains(upstream.lastProxy, "account-proxy") {
		t.Fatal("the account's own proxy must not win over the configured dynamic proxy")
	}
}

func TestProbeOneFallsBackToTheAccountProxy(t *testing.T) {
	upstream := &captureUpstream{status: http.StatusOK, headers: map[string]string{"X-Codex-Turn-State": "state"}}
	account := codexOAuthAccount()
	account.ProxyID = turnStateInt64Ptr(7)
	account.Proxy = &Proxy{ID: 7, Protocol: "http", Host: "account-proxy.example", Port: 8080}

	svc := probeService(upstream, account, CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})
	svc.probeOne(context.Background(), account.ID, svc.cfg)

	if !strings.Contains(upstream.lastProxy, "account-proxy.example:8080") {
		t.Fatalf("proxy = %q, want the account proxy as the fallback", upstream.lastProxy)
	}
}

func turnStateInt64Ptr(v int64) *int64 { return &v }

func TestProbeOneRecordsFailuresInTheTimeline(t *testing.T) {
	upstream := &captureUpstream{status: http.StatusUnauthorized, body: `{"detail":"Unauthorized"}`}
	account := codexOAuthAccount()
	repo := newFakeTurnStateRepo()
	svc := NewCodexTurnStateService(repo, &staticAccountRepo{account: account}, nil, upstream)
	svc.cfg = NormalizeCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, GroupIDs: []int64{24}})
	svc.cfgAt = time.Now()

	svc.probeOne(context.Background(), account.ID, svc.cfg)

	records := repo.snapshot()
	if len(records) != 1 {
		t.Fatalf("stored %d records, want 1 failure row", len(records))
	}
	if records[0].Status != CodexTurnStateStatusFailed {
		t.Errorf("status = %q, want failed", records[0].Status)
	}
	if records[0].Error == "" {
		t.Error("a failure must record why it failed")
	}
	if _, _, ok := svc.InjectionHeader(account.ID, ""); ok {
		t.Error("a failed probe must not create an injectable state")
	}
}
