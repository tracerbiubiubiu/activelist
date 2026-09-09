// 数据 CRUD 端点（M-A3；A2/A4）。薄层：绑定/游标解析/状态码映射，业务在 service。
// 鉴权随 M-A6 AK/SK 中间件（当前 /api/v1 仅限内网开发态调用）。
package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// insertData 插入数据（201；类型不存在 404 / 已废弃 409 / 校验失败 422）。
func (d *Deps) insertData(c *gin.Context) {
	var in service.InsertInput
	if err := c.ShouldBindJSON(&in); err != nil || in.Data == nil {
		BadRequest(c, "请求体解析失败（data 必填且须为 JSON 对象）")
		return
	}
	doc, err := d.Data.Insert(c.Request.Context(), c.Param("typeName"), in, operatorFallback)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	Created(c, doc)
}

// getData 单查（软删行可见，status 标注现态）。
func (d *Deps) getData(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	doc, err := d.Data.Get(c.Request.Context(), c.Param("typeName"), id)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, doc)
}

// listData 列表（keyset 分页：after_created_at/after_id 须成对，缺一 400；
// page_size 钳制到 [1, max] 并回显生效值——调用方无需猜测实际页大小）。
// 满页才给 next_cursor；下一页空列表时 cursor 为 null，遍历终止。
func (d *Deps) listData(c *gin.Context) {
	cur, ok := parseCursor(c)
	if !ok {
		return
	}
	// page_size 先解析后钳制（解析失败 400 不触达 service；钳制参数来自 config）
	pageSize := 0
	if raw := c.Query("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			BadRequest(c, "page_size 须为整数")
			return
		}
		pageSize = n
	}
	defSize, maxSize := d.Data.PageLimits()
	if pageSize == 0 {
		pageSize = defSize
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > maxSize {
		pageSize = maxSize
	}

	docs, err := d.Data.List(c.Request.Context(), c.Param("typeName"), cur, pageSize)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	if docs == nil {
		docs = []repository.Document{}
	}
	var next any
	if len(docs) == pageSize {
		last := docs[len(docs)-1]
		next = gin.H{"after_created_at": last.CreatedAt, "after_id": last.ID}
	}
	OK(c, gin.H{"list": docs, "page_size": pageSize, "next_cursor": next})
}

// updateData 更新（body 携带 version 乐观锁；读-合并-全量校验在 service）。
func (d *Deps) updateData(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in service.UpdateInput
	if err := c.ShouldBindJSON(&in); err != nil || in.Data == nil {
		BadRequest(c, "请求体解析失败（data 必填且须为 JSON 对象）")
		return
	}
	doc, err := d.Data.Update(c.Request.Context(), c.Param("typeName"), id, in, operatorFallback)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, doc)
}

// deleteData 软删除（幂等；返回删除后完整文档）。
func (d *Deps) deleteData(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	doc, err := d.Data.SoftDelete(c.Request.Context(), c.Param("typeName"), id, operatorFallback)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, doc)
}

// restoreData 恢复软删数据（幂等；返回恢复后完整文档）。
func (d *Deps) restoreData(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	doc, err := d.Data.Restore(c.Request.Context(), c.Param("typeName"), id, operatorFallback)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, doc)
}

// pathID 路径参数 id 解析（非整数 400）。
func pathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "路径参数 id 须为整数")
		return 0, false
	}
	return id, true
}

// parseCursor keyset 游标解析：after_created_at(RFC3339) + after_id 须成对出现
// （§4——缺一 400，避免「只给时间」语义含糊）。
func parseCursor(c *gin.Context) (*repository.Cursor, bool) {
	ac, hasAC := c.GetQuery("after_created_at")
	aid, hasAI := c.GetQuery("after_id")
	if hasAC != hasAI {
		BadRequest(c, "keyset 游标须成对出现（after_created_at + after_id）")
		return nil, false
	}
	if !hasAC {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, ac)
	if err != nil {
		BadRequest(c, "after_created_at 须为 RFC3339 时间")
		return nil, false
	}
	id, err := strconv.ParseInt(aid, 10, 64)
	if err != nil {
		BadRequest(c, "after_id 须为整数")
		return nil, false
	}
	return &repository.Cursor{CreatedAt: t, ID: id}, true
}

// exportData 全量导出（含软删行）。文件本体 = 裸 JSON 数组（实现拍板：信封包
// 文件体破坏流式与导出/导入对称性）——本端点不走 §6.8 信封；流中途错误只能在
// 响应头之后截断（前置 gate 错误仍走信封错误响应）。
func (d *Deps) exportData(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if err := d.Data.Export(c.Request.Context(), c.Param("typeName"), c.Writer); err != nil {
		Fail(c, asAppErr(err))
	}
}

// importData 全量替换导入（body = 导出同构的 JSON 数组，流式分批处理）。
func (d *Deps) importData(c *gin.Context) {
	res, err := d.Data.Import(c.Request.Context(), c.Param("typeName"), c.Request.Body, operatorFallback)
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, res)
}
