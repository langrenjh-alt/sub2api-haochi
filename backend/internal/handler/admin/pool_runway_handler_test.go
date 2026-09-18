package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func TestPoolRunwayRequiresAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"", "user", "admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
					c.Set(string(middleware.ContextKeyUserRole), role)
				}
			})
			h := &PoolRunwayHandler{}
			r.GET("/", h.Overview)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/?policy=invalid", nil))
			want := 400
			if role == "" {
				want = 401
			} else if role == "user" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("role=%q status=%d want=%d", role, w.Code, want)
			}
		})
	}
}

func TestPoolRunwayConfigRequiresAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"", "user"} {
		for _, method := range []string{"GET", "PUT"} {
			t.Run(method+"/"+role, func(t *testing.T) {
				r := gin.New()
				r.Use(func(c *gin.Context) {
					if role != "" {
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
						c.Set(string(middleware.ContextKeyUserRole), role)
					}
				})
				h := &PoolRunwayHandler{}
				r.GET("/", h.Config)
				r.PUT("/", h.Configure)
				out := httptest.NewRecorder()
				r.ServeHTTP(out, httptest.NewRequest(method, "/", strings.NewReader(`{"group_id":89,"revision":0}`)))
				want := 401
				if role == "user" {
					want = 403
				}
				if out.Code != want {
					t.Fatal(out.Code, want)
				}
			})
		}
	}
}

func TestPoolRunwayConfigRejectsMalformedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{}`, `{"group_id":0,"revision":0}`, `{"group_id":-1,"revision":0}`,
		`{"group_id":89}`, `{"group_id":89,"revision":null}`, `{"group_id":89,"revision":-1}`,
		`{"group_id":true,"revision":0}`, `{"group_id":89.5,"revision":0}`, `{"group_id":89,"revision":0,"schedulable":true}`,
		`{"group_id":89,"revision":0} {}`, strings.Repeat(" ", 1025) + `{}`} {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
			c.Set(string(middleware.ContextKeyUserRole), "admin")
		})
		h := &PoolRunwayHandler{}
		r.PUT("/", h.Configure)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		if out.Code != 400 {
			t.Fatalf("body=%q status=%d", body, out.Code)
		}
	}
}
