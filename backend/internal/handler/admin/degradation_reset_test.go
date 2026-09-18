package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type resetDegradationRepo struct {
	service.DegradationRepository
	calls int
}

func (r *resetDegradationRepo) ResetPublicStats(_ context.Context, actor int64) (time.Time, error) {
	r.calls++
	return time.Date(2026, 9, 16, 16, 0, 0, 0, time.UTC), nil
}

func TestDegradationResetRequiresAdminAndConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		role, body string
		status     int
		calls      int
	}{
		{"", `{"confirm":true}`, 401, 0},
		{"user", `{"confirm":true}`, 403, 0},
		{"admin", `{}`, 400, 0},
		{"admin", `{"confirm":false}`, 400, 0},
		{"admin", `{broken`, 400, 0},
		{"admin", `{"confirm":true}`, 200, 1},
	} {
		t.Run(tc.role+tc.body, func(t *testing.T) {
			repo := &resetDegradationRepo{}
			h := NewDegradationHandler(service.NewDegradationService(repo))
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				if tc.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			engine.POST("/reset", h.ResetPublicStats)
			req := httptest.NewRequest("POST", "/reset", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.calls, repo.calls)
			if tc.calls > 0 {
				require.Contains(t, w.Body.String(), "reset_at")
			}
		})
	}
}
