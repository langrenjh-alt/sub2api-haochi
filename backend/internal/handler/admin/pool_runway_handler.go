package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/poolrunway"
	"github.com/gin-gonic/gin"
)

type PoolRunwayHandler struct{ worker *poolrunway.Worker }

func NewPoolRunwayHandler(db *sql.DB) *PoolRunwayHandler {
	w := poolrunway.New(db)
	w.Start()
	return &PoolRunwayHandler{worker: w}
}
func (h *PoolRunwayHandler) Config(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	cfg, err := h.worker.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取监控分组配置失败")
		return
	}
	response.Success(c, cfg)
}
func (h *PoolRunwayHandler) Configure(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var req struct {
		GroupID  int64  `json:"group_id"`
		Revision *int64 `json:"revision"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&req) != nil || req.GroupID <= 0 || req.Revision == nil || *req.Revision < 0 || decoder.Decode(&extra) != io.EOF {
		response.BadRequest(c, "请选择有效分组并携带配置版本")
		return
	}
	err := h.worker.Configure(c.Request.Context(), req.GroupID, *req.Revision)
	switch {
	case errors.Is(err, poolrunway.ErrInvalidGroup):
		response.BadRequest(c, "分组不存在、已删除或不是 OpenAI 分组")
		return
	case errors.Is(err, poolrunway.ErrConfigConflict):
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "配置已被更新，请刷新后重新选择"})
		return
	case errors.Is(err, poolrunway.ErrCollectorBusy):
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "定时采样正在完成，请稍后重试保存"})
		return
	case err != nil:
		response.InternalError(c, "保存监控配置失败")
		return
	}
	// Return the committed revision, not another read which might fail after commit.
	response.Success(c, gin.H{"group_id": req.GroupID, "revision": *req.Revision + 1})
}
func (h *PoolRunwayHandler) Stop() {
	if h != nil && h.worker != nil {
		h.worker.Stop()
	}
}
func (h *PoolRunwayHandler) Overview(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	policy := c.DefaultQuery("policy", poolrunway.DefaultPolicy)
	if policy != poolrunway.DefaultPolicy && policy != "known_only" {
		response.BadRequest(c, "无效估算策略")
		return
	}
	o, err := h.worker.Overview(c.Request.Context(), policy)
	if err != nil {
		response.InternalError(c, "读取监控快照失败；请保留上次记录")
		return
	}
	raw, err := poolrunway.PublicJSON(o)
	if err != nil {
		response.InternalError(c, "监控输出校验失败")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", append(append([]byte(`{"code":0,"data":`), raw...), '}'))
}
