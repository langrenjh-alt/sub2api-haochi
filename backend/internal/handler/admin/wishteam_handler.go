package admin

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/wishteam"
	"github.com/gin-gonic/gin"
)

type WishTeamHandler struct{ worker *wishteam.Worker }

func NewWishTeamHandler(db *sql.DB) *WishTeamHandler {
	w := wishteam.New(db)
	w.Start()
	return &WishTeamHandler{w}
}
func (h *WishTeamHandler) Stop() {
	if h != nil && h.worker != nil {
		h.worker.Stop()
	}
}

func (h *WishTeamHandler) Overview(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	ctx := c.Request.Context()
	cfg, err := h.worker.Config(ctx)
	if err != nil {
		response.InternalError(c, "读取 WishTeam5X 配置失败")
		return
	}
	groups, err := h.worker.Groups(ctx)
	if err != nil {
		response.InternalError(c, "读取监控分组失败")
		return
	}
	runs, err := h.worker.Runs(ctx)
	if err != nil {
		response.InternalError(c, "读取巡查进度失败")
		return
	}
	response.Success(c, gin.H{"config": cfg, "groups": groups, "runs": runs, "provider": wishteam.Endpoint})
}

func (h *WishTeamHandler) Configure(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var cfg wishteam.Config
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "无效配置")
		return
	}
	if err := h.worker.Configure(c.Request.Context(), cfg); err != nil {
		response.BadRequest(c, "保存失败：请选择有效的 OpenAI 分组，间隔为 1–1440 分钟")
		return
	}
	h.Overview(c)
}

func (h *WishTeamHandler) Run(c *gin.Context) {
	actor, ok := intelligentAdminActor(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm {
		response.BadRequest(c, "请确认检测并复活所选分组")
		return
	}
	id, err := h.worker.StartRun(c.Request.Context(), actor, false)
	if err != nil {
		response.BadRequest(c, "请先保存有效分组，并等待当前任务完成")
		return
	}
	response.Accepted(c, gin.H{"run_id": id})
}

func (h *WishTeamHandler) Items(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	page := 1
	if p := c.Query("page"); p != "" {
		page, err = strconv.Atoi(p)
	}
	if err != nil || id < 1 || page < 1 || page > 1000000 {
		response.BadRequest(c, "无效分页参数")
		return
	}
	items, total, err := h.worker.Items(c.Request.Context(), id, page)
	if err != nil {
		response.InternalError(c, "读取子号巡查记录失败")
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": 50})
}

func (h *WishTeamHandler) Archive(c *gin.Context) {
	if _, ok := intelligentAdminActor(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "无效记录编号")
		return
	}
	data, err := h.worker.Archive(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "此记录没有已完成的替换存档")
		return
	}
	response.Success(c, data)
}
