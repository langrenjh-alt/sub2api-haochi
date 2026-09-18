package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWishTeamEveryEndpointRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WishTeamHandler{} // nil worker proves denial precedes all storage access
	for name, handler := range map[string]gin.HandlerFunc{
		"overview": h.Overview, "config": h.Configure, "run": h.Run, "items": h.Items, "archive": h.Archive,
	} {
		for _, role := range []string{"", "user"} {
			t.Run(name+"/"+role, func(t *testing.T) {
				r := gin.New()
				r.Use(func(c *gin.Context) {
					if role != "" {
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
						c.Set(string(middleware.ContextKeyUserRole), role)
					}
				})
				r.POST("/", handler)
				out := httptest.NewRecorder()
				r.ServeHTTP(out, httptest.NewRequest("POST", "/", strings.NewReader(`{"confirm":true}`)))
				expected := 401
				if role != "" {
					expected = 403
				}
				require.Equal(t, expected, out.Code)
			})
		}
	}
}

func TestWishTeamRunRequiresExplicitConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WishTeamHandler{}
	for _, body := range []string{`{}`, `{"confirm":false}`, `{"confirm":"true"}`, `broken`} {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
			c.Set(string(middleware.ContextKeyUserRole), "admin")
		})
		r.POST("/", h.Run)
		out := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(out, req)
		require.Equal(t, 400, out.Code)
	}
}
