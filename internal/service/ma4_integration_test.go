//go:build integration

// M-A4 / A3 验收集成验证（真 PG）：方案 D 演进语义——加 optional 零迁移可读可写 /
// 加 required 旧数据懒执行 422（NEW_REQUIRED_FIELD 含迁移指引）/ 移除字段懒执行
// （FIELD_DEPRECATED 按字段史判定，拼写错误仍 VALIDATION_ERROR）/ 元数据乐观锁
// 并发演进 409 / 废弃类型拒演进 / 变更历史（含 HTTP e2e）。
package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func ma4Fields() []meta.Field {
	return []meta.Field{
		{Name: "name", Type: "string", Required: true},
		{Name: "qty", Type: "int"},
	}
}

// A3① 加 optional 字段：旧数据零迁移可读可写；新写入可选给字段。
func TestA3_EvolveOptionalCompat(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "ev_opt", Fields: ma4Fields()}, "system")
	require.NoError(t, err)
	doc, err := dsvc.Insert(ctx, "ev_opt", service.InsertInput{
		Data: map[string]any{"name": "a"}}, "system")
	require.NoError(t, err)

	def, err := tsvc.Evolve(ctx, "ev_opt", service.EvolveInput{
		Fields: append(ma4Fields(), meta.Field{Name: "tag", Type: "string"}),
		Version: 1}, "system")
	require.NoError(t, err)
	require.EqualValues(t, 2, def.Version)
	require.Len(t, def.Fields, 3)

	// 旧数据零迁移：读不校验、更新按新 schema 校验通过（tag optional 不强求）
	got, err := dsvc.Get(ctx, "ev_opt", doc.ID)
	require.NoError(t, err)
	require.Equal(t, "a", got.Data["name"])
	upd, err := dsvc.Update(ctx, "ev_opt", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "system")
	require.NoError(t, err)
	require.Nil(t, upd.Data["tag"])

	// 新插入可选给 tag
	_, err = dsvc.Insert(ctx, "ev_opt", service.InsertInput{
		Data: map[string]any{"name": "b"}}, "system")
	require.NoError(t, err)
	_, err = dsvc.Insert(ctx, "ev_opt", service.InsertInput{
		Data: map[string]any{"name": "c", "tag": "t"}}, "system")
	require.NoError(t, err)
}

// A3② 加 required 字段：新插入强制校验；旧数据更新懒执行 422（NEW_REQUIRED_FIELD
// 含迁移指引），补齐后可写；既有 optional 收紧为 required 同路径。
func TestA3_EvolveRequiredLazy(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "ev_req", Fields: ma4Fields()}, "system")
	require.NoError(t, err)
	doc, err := dsvc.Insert(ctx, "ev_req", service.InsertInput{
		Data: map[string]any{"name": "a"}}, "system")
	require.NoError(t, err)

	_, err = tsvc.Evolve(ctx, "ev_req", service.EvolveInput{
		Fields: append(ma4Fields(), meta.Field{Name: "level", Type: "int", Required: true}),
		Version: 1}, "system")
	require.NoError(t, err)

	// 新插入强制校验（普通 VALIDATION_ERROR——新建数据本就该全量给齐）
	_, err = dsvc.Insert(ctx, "ev_req", service.InsertInput{
		Data: map[string]any{"name": "b"}}, "system")
	require.Equal(t, 422, mustAE(t, err).HTTP)
	require.Equal(t, "VALIDATION_ERROR", mustAE(t, err).Code)
	_, err = dsvc.Insert(ctx, "ev_req", service.InsertInput{
		Data: map[string]any{"name": "b", "level": float64(1)}}, "system")
	require.NoError(t, err)

	// 旧数据更新 → 懒执行 422 + 迁移指引（A3：错误信息含迁移指引）
	_, err = dsvc.Update(ctx, "ev_req", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(1)}, Version: 1}, "system")
	e := mustAE(t, err)
	require.Equal(t, 422, e.HTTP)
	require.Equal(t, "NEW_REQUIRED_FIELD", e.Code)
	require.Equal(t, "level", e.Detail["field"])
	require.Contains(t, e.Msg, "补齐", "错误信息须含迁移指引")

	// 手工补齐后可写（合并保留已补字段）
	upd, err := dsvc.Update(ctx, "ev_req", doc.ID, service.UpdateInput{
		Data: map[string]any{"level": float64(3)}, Version: 1}, "system")
	require.NoError(t, err)
	require.Equal(t, float64(3), upd.Data["level"])

	// 既有 optional 收紧 required：旧行缺 qty → 同路径懒执行
	_, err = tsvc.Evolve(ctx, "ev_req", service.EvolveInput{
		Fields: []meta.Field{
			{Name: "name", Type: "string", Required: true},
			{Name: "level", Type: "int", Required: true},
			{Name: "qty", Type: "int", Required: true},
		}, Version: 2}, "system")
	require.NoError(t, err)
	_, err = dsvc.Update(ctx, "ev_req", doc.ID, service.UpdateInput{
		Data: map[string]any{"level": float64(4)}, Version: 2}, "system")
	require.Equal(t, "NEW_REQUIRED_FIELD", mustAE(t, err).Code)
	require.Equal(t, "qty", mustAE(t, err).Detail["field"])
}

// A3③ 移除字段：旧数据更新/新插入遇已移除字段 → FIELD_DEPRECATED（按字段史判定）；
// 真拼写错误仍 VALIDATION_ERROR。
func TestA3_EvolveRemoveFieldLazy(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "ev_rm", Fields: ma4Fields()}, "system")
	require.NoError(t, err)
	doc, err := dsvc.Insert(ctx, "ev_rm", service.InsertInput{
		Data: map[string]any{"name": "a", "qty": float64(1)}}, "system")
	require.NoError(t, err)

	_, err = tsvc.Evolve(ctx, "ev_rm", service.EvolveInput{
		Fields: ma4Fields()[:1], Version: 1}, "system") // 移除 qty
	require.NoError(t, err)

	// 旧数据更新：合并后携带已移除字段 → FIELD_DEPRECATED + 清理指引
	_, err = dsvc.Update(ctx, "ev_rm", doc.ID, service.UpdateInput{
		Data: map[string]any{"name": "b"}, Version: 1}, "system")
	e := mustAE(t, err)
	require.Equal(t, 422, e.HTTP)
	require.Equal(t, "FIELD_DEPRECATED", e.Code)
	require.Equal(t, "qty", e.Detail["field"])
	require.Contains(t, e.Msg, "移除")

	// 新插入携带已移除字段同样按字段史判定
	_, err = dsvc.Insert(ctx, "ev_rm", service.InsertInput{
		Data: map[string]any{"name": "c", "qty": float64(2)}}, "system")
	require.Equal(t, "FIELD_DEPRECATED", mustAE(t, err).Code)

	// 真拼写错误 → VALIDATION_ERROR 未知字段
	_, err = dsvc.Insert(ctx, "ev_rm", service.InsertInput{
		Data: map[string]any{"name": "d", "qtyy": float64(2)}}, "system")
	require.Equal(t, "VALIDATION_ERROR", mustAE(t, err).Code)

	// 移除字段后合法写入照常
	_, err = dsvc.Insert(ctx, "ev_rm", service.InsertInput{
		Data: map[string]any{"name": "e"}}, "system")
	require.NoError(t, err)
}

// A3④/§7 元数据乐观锁：陈旧版本 409 须重读重提；废弃类型拒演进。
func TestA3_EvolveConflictAndDeprecated(t *testing.T) {
	_, tsvc := setupPG(t)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "ev_lock", Fields: ma4Fields()}, "system")
	require.NoError(t, err)

	_, err = tsvc.Evolve(ctx, "ev_lock", service.EvolveInput{Fields: ma4Fields(), Version: 9}, "system")
	e := mustAE(t, err)
	require.Equal(t, 409, e.HTTP)
	require.Equal(t, "CONFLICT", e.Code)
	require.EqualValues(t, 9, e.Detail["expected_version"])

	def, err := tsvc.Evolve(ctx, "ev_lock", service.EvolveInput{
		Fields: append(ma4Fields(), meta.Field{Name: "tag", Type: "string"}), Version: 1}, "system")
	require.NoError(t, err)
	require.EqualValues(t, 2, def.Version)

	// 演进后旧版本复用 → 409
	_, err = tsvc.Evolve(ctx, "ev_lock", service.EvolveInput{Fields: ma4Fields(), Version: 1}, "system")
	require.Equal(t, "CONFLICT", mustAE(t, err).Code)

	// 废弃后拒绝演进
	_, err = tsvc.Deprecate(ctx, "ev_lock", "system")
	require.NoError(t, err)
	_, err = tsvc.Evolve(ctx, "ev_lock", service.EvolveInput{Fields: ma4Fields(), Version: 2}, "system")
	require.Equal(t, "TYPE_DEPRECATED", mustAE(t, err).Code)
}

// A3⑤ 变更历史：register/evolve/deprecate 三 op 齐全、新→旧排序、未知类型 404。
func TestA3_HistoryService(t *testing.T) {
	_, tsvc := setupPG(t)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "ev_hist", Fields: ma4Fields()}, "system")
	require.NoError(t, err)
	_, err = tsvc.Evolve(ctx, "ev_hist", service.EvolveInput{
		Fields: append(ma4Fields(), meta.Field{Name: "tag", Type: "string"}), Version: 1}, "system")
	require.NoError(t, err)
	_, err = tsvc.Deprecate(ctx, "ev_hist", "system")
	require.NoError(t, err)

	hist, err := tsvc.History(ctx, "ev_hist")
	require.NoError(t, err)
	require.Len(t, hist, 3)
	require.Equal(t, []string{"deprecate", "evolve", "register"},
		[]string{hist[0].Op, hist[1].Op, hist[2].Op})
	require.Len(t, hist[0].Fields, 3) // deprecate 存当时 schema
	require.Len(t, hist[1].Fields, 3) // evolve 后 3 字段
	require.Len(t, hist[2].Fields, 2) // 注册时 2 字段
	require.Equal(t, "system", hist[0].ChangedBy)

	_, err = tsvc.History(ctx, "ghost_type")
	require.Equal(t, "TYPE_NOT_FOUND", mustAE(t, err).Code)
}

// HTTP e2e：演进/历史端点 + 信封（200/409/404/400）。
func TestA3_HTTPSchemaHistory(t *testing.T) {
	pool, tsvc := setupPG(t)
	gin.SetMode(gin.TestMode)
	r := handler.New(handler.Deps{Types: tsvc, Data: newDataSvc(pool)})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types",
		strings.NewReader(`{"type_name":"ev_e2e","fields":[{"name":"name","type":"string","required":true}]}`)))
	require.Equal(t, http.StatusCreated, w.Code)

	// 演进 200 + 版本递增
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types/ev_e2e/schema",
		strings.NewReader(`{"fields":[{"name":"name","type":"string","required":true},{"name":"qty","type":"int"}],"version":1}`)))
	require.Equal(t, http.StatusOK, w.Code)
	var env struct {
		Data struct {
			Version int64 `json:"version"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.EqualValues(t, 2, env.Data.Version)

	// 陈旧版本 → 409 CONFLICT
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types/ev_e2e/schema",
		strings.NewReader(`{"fields":[{"name":"name","type":"string","required":true}],"version":1}`)))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), `"error_code":"CONFLICT"`)

	// 历史 200：2 条，新→旧
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/types/ev_e2e/history", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var histEnv struct {
		Data struct {
			List []struct {
				Op string `json:"op"`
			} `json:"list"`
			Total int `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &histEnv))
	require.Equal(t, 2, histEnv.Data.Total)
	require.Equal(t, "evolve", histEnv.Data.List[0].Op)
	require.Equal(t, "register", histEnv.Data.List[1].Op)

	// 未知类型 404 / 非法 body 400
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types/ghost/schema",
		strings.NewReader(`{"fields":[{"name":"x","type":"int"}],"version":1}`)))
	require.Equal(t, http.StatusNotFound, w.Code)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types/ev_e2e/schema",
		strings.NewReader(`{"version":2}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}
