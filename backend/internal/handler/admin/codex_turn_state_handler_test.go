package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type historyClearTestRepo struct {
	service.CodexTurnStateRepository
	called bool
}

func (r *historyClearTestRepo) ClearHistory(context.Context, []int64) (int64, int64, error) {
	r.called = true
	return 12, 2, nil
}

func TestCodexTurnStateClearHistoryRequiresAdminAndConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		role, body string
		status     int
		called     bool
	}{
		{"", `{"confirm":true}`, 401, false},
		{"user", `{"confirm":true}`, 403, false},
		{"admin", `{"confirm":false}`, 400, false},
		{"admin", `{}`, 400, false},
		{"admin", `{"confirm":true}`, 200, true},
	} {
		t.Run(tc.role+tc.body, func(t *testing.T) {
			repo := &historyClearTestRepo{}
			h := NewCodexTurnStateHandler(service.NewCodexTurnStateService(repo, nil, nil, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			r.DELETE("/history", h.ClearHistory)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("DELETE", "/history", strings.NewReader(tc.body)))
			if w.Code != tc.status || repo.called != tc.called {
				t.Fatalf("status=%d called=%v body=%s", w.Code, repo.called, w.Body)
			}
		})
	}
}
