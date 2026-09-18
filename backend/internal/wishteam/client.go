package wishteam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type client struct {
	base string
	http *http.Client
}

func newClient() *client {
	return &client{Endpoint, &http.Client{
		Timeout: 30 * time.Second,
		// Never follow a redirect with account secrets or a secret task ID.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type remoteError struct {
	retry     int
	ambiguous bool
	message   string
}

func (e *remoteError) Error() string { return e.message }

func (c *client) request(ctx context.Context, method, path string, body any) (*RemoteReply, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, errors.New("创建上游请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("Pragma", "no-cache")
	resp, err := c.http.Do(req)
	if err != nil {
		// Do not include net/url errors: task IDs are capability secrets.
		return nil, &remoteError{ambiguous: method == http.MethodPost, message: "上游连接中断；未重复提交复活请求"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (24<<20)+1))
	if err != nil || len(raw) > 24<<20 {
		return nil, &remoteError{ambiguous: method == http.MethodPost, message: "上游响应读取失败或过大"}
	}
	var out RemoteReply
	decodeErr := json.Unmarshal(raw, &out)
	if resp.StatusCode == http.StatusTooManyRequests || (decodeErr == nil && !out.Success && out.RetryAfter > 0) {
		retry := out.RetryAfter
		if retry <= 0 {
			retry, _ = strconv.Atoi(resp.Header.Get("Retry-After"))
		}
		if retry < 30 {
			retry = 30
		}
		if retry > 86400 {
			retry = 86400
		}
		return nil, &remoteError{retry: retry, message: "上游限频或权益传播等待"}
	}
	if resp.StatusCode != 200 || decodeErr != nil || !out.Success {
		return nil, &remoteError{ambiguous: method == http.MethodPost, message: fmt.Sprintf("上游未返回成功结果（HTTP %d），旧号保留", resp.StatusCode)}
	}
	return &out, nil
}

func (c *client) submit(ctx context.Context, doc Document) (*RemoteReply, error) {
	return c.request(ctx, "POST", "/api/revive/batch/jobs", map[string]any{"sub2": doc})
}
func (c *client) poll(ctx context.Context, id string, result bool) (*RemoteReply, error) {
	path := "/api/revive/batch/jobs/" + url.PathEscape(id)
	if result {
		path += "/result"
	}
	return c.request(ctx, "GET", path, nil)
}
