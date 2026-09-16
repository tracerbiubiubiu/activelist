// Package handler HTTP 层（薄：绑定/映射，业务在 service——结构基线）。
// M-A1 健康探针 + M-A2 类型管理端点；AK/SK 验签与访问日志中间件随 M-A6
// （当前 /api/v1 仅限内网开发态调用，不上线）。
package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/middleware"
	"github.com/tracerbiubiubiu/activelist/internal/service"
	"github.com/tracerbiubiubiu/activelist/internal/validation"
)

// operatorFallback X-Operator 缺省值（AKSKAuth+Operator 中间件缺头时的兜底，
// 对齐 16 号 §9 访问日志 operator 口径）。
const operatorFallback = "system"

// currentOperator 取经签名校验透传的操作者；直连开发态（未挂验签）回退 system。
func currentOperator(c *gin.Context) string {
	if v := c.GetString("operator"); v != "" {
		return v
	}
	return operatorFallback
}

// Deps handler 依赖。Ready 为 readyz 探针（检 PG 可查询）；nil = 恒就绪（测试用）。
// Callers 为 AK/SK 验签密钥环（AK→SK）：非空时 /api/v1 挂验签 + Operator 中间件
// （M-A6），为空时不挂（内网开发态/测试构造）；生产装配在 app 层对空环 fail-closed。
// MaxBodyBytes = 验签读体上限（= cfg.Business.ImportMaxBytes，勿用 aksk 默认 8MB）。
// Logger 非 nil 时启用统一访问日志出口（16 号 §9）。
type Deps struct {
	Types        *service.TypeService
	Data         *service.DataService
	Ready        func() error
	Callers      map[string][]byte
	MaxBodyBytes int64
	Logger       *slog.Logger
}

// New 构造路由引擎。
func New(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID())
	if d.Logger != nil {
		r.Use(middleware.AccessLog(d.Logger))
	}

	// 探针裸露（不进业务组）——容器编排存活/就绪检查用。
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		if d.Ready != nil {
			if err := d.Ready(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unready", "err": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	// 类型管理（M-A2；A1）。M-A6：Callers 非空时 /api/v1 走 AK/SK 验签（2026-09-16
	// 统一批：AKSKFail 统一失败响应 + caller/operator 归因由 GinMiddleware 写入）。
	v1 := r.Group("/api/v1")
	if len(d.Callers) > 0 {
		v1.Use(middleware.AKSKAuth(d.Callers, d.MaxBodyBytes, d.Logger))
	}
	{
		types := v1.Group("/admin/types")
		{
			types.POST("", d.registerType)
			types.GET("", d.listTypes)
			types.GET("/rules", d.typeRules) // 静态优先于 /:typeName
			types.GET("/:typeName", d.getType)
			types.POST("/:typeName/deprecate", d.deprecateType)
			types.POST("/:typeName/schema", d.evolveType)
			types.GET("/:typeName/history", d.listTypeHistory)
		}

		// 数据 CRUD（M-A3；A2/A4）。软删行单查可见、列表默认排除（§7）。
		data := v1.Group("/data")
		{
			data.POST("/:typeName", d.insertData)
			data.GET("/:typeName", d.listData)
			data.GET("/:typeName/:id", d.getData)
			data.POST("/:typeName/:id/update", d.updateData)
			data.POST("/:typeName/:id/delete", d.deleteData)
			data.POST("/:typeName/:id/restore", d.restoreData)

			// 导入导出（M-A5；A5）。export=静态段与 :id 参数同级（gin 静态优先）
			data.GET("/:typeName/export", d.exportData)
			data.POST("/:typeName/import", d.importData)
		}
	}
	return r
}

// registerType 注册类型（200；重复 409；非法 422——A1）。
func (d *Deps) registerType(c *gin.Context) {
	var in service.RegisterInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "请求体解析失败（type_name 与 fields 必填）")
		return
	}
	def, err := d.Types.Register(c.Request.Context(), in, currentOperator(c))
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, def)
}

// listTypes 类型列表。
func (d *Deps) listTypes(c *gin.Context) {
	list, err := d.Types.List(c.Request.Context())
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	if list == nil {
		list = []meta.Definition{}
	}
	OK(c, gin.H{"list": list, "total": len(list)})
}

// typeRules 类型与字段创建规则（前端创建类型弹窗展示用；纯静态数据无需 service）。
func (d *Deps) typeRules(c *gin.Context) {
	OK(c, validation.GetRules())
}

// getType 查类型当前 schema 定义（不存在 404）。
func (d *Deps) getType(c *gin.Context) {
	def, err := d.Types.Get(c.Request.Context(), c.Param("typeName"))
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, def)
}

// deprecateType 废弃类型（幂等；不存在 404）。
func (d *Deps) deprecateType(c *gin.Context) {
	def, err := d.Types.Deprecate(c.Request.Context(), c.Param("typeName"), currentOperator(c))
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, def)
}

// evolveType schema 演进（方案 D；不存在 404 / 已废弃 409 / 版本冲突 409 / 非法 422）。
func (d *Deps) evolveType(c *gin.Context) {
	var in service.EvolveInput
	if err := c.ShouldBindJSON(&in); err != nil || len(in.Fields) == 0 {
		BadRequest(c, "请求体解析失败（fields 全量定义与 version 必填）")
		return
	}
	def, err := d.Types.Evolve(c.Request.Context(), c.Param("typeName"), in, currentOperator(c))
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, def)
}

// listTypeHistory schema 变更历史（新→旧）。
func (d *Deps) listTypeHistory(c *gin.Context) {
	list, err := d.Types.History(c.Request.Context(), c.Param("typeName"))
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	if list == nil {
		list = []meta.HistoryEntry{}
	}
	OK(c, gin.H{"list": list, "total": len(list)})
}

// asAppErr 非 *apperr.Error 的意外错误兜底为 500（防内部细节泄漏）。
// errors.As 容忍中间层包装（%w）——直接类型断言遇包装会误降级为无上下文 500。
func asAppErr(err error) *apperr.Error {
	var e *apperr.Error
	if errors.As(err, &e) {
		return e
	}
	return apperr.New(500, apperr.CodeInternal, "内部错误")
}
