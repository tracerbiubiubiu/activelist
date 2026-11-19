//go:build integration

// 软删/恢复可选版本保护（2026-11-19 批——CMP-R02 同树增量）：
// 新客户端带现有 JSON number version 时，active→deleted / deleted→active 的实际迁移
// 须行锁内核验版本相等，不匹配 409（与 Update 同类型）零写；目标态相同的重复请求
// 保持幂等，版本判断仅约束实际迁移；旧客户端无版本（DELETE 空 body / {}）维持旧契约；
// 非正 version 按 Update 现行验证规则 422。另验证 full replace 导入重用 id 时
// revision 不重复（全库序列不复用旧值，导入前持有版本全部失效）。
package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// verPtr 版本保护入参便利构造（nil = 旧契约无版本保护）。
func verPtr(v int64) *int64 { return &v }

// requireVersionConflict 断言与 Update 同型的版本不匹配 409（含回显坐标）。
func requireVersionConflict(t *testing.T, err error, expected, current int64) {
	t.Helper()
	e := mustAE(t, err)
	require.Equal(t, 409, e.HTTP)
	require.Equal(t, apperr.CodeConflict, e.Code)
	require.Equal(t, "版本不匹配（数据已被并发修改）", e.Msg)
	require.EqualValues(t, expected, e.Detail["expected_version"])
	require.EqualValues(t, current, e.Detail["current_version"])
}

// TestSoftDeleteRestore_VersionGuard 守卫矩阵：stale 409 零写 / 非正 422 /
// 相等才迁移 / 幂等不受版本约束 / nil 旧契约 / 404 定位不变。
func TestSoftDeleteRestore_VersionGuard(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "verdel_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "verdel_item", 1, "a")
	v1 := doc.Version

	// 非正 version：按现行 Update 的验证规则拒绝（422 VALIDATION_ERROR，field=version）
	for _, bad := range []int64{0, -3} {
		_, err = dsvc.SoftDelete(ctx, "verdel_item", doc.ID, "system", verPtr(bad))
		e := mustAE(t, err)
		require.Equal(t, 422, e.HTTP)
		require.Equal(t, apperr.CodeValidation, e.Code)
		require.Equal(t, "version", e.Detail["field"])
		_, err = dsvc.Restore(ctx, "verdel_item", doc.ID, "system", verPtr(bad))
		require.Equal(t, 422, mustAE(t, err).HTTP)
	}

	// stale 删除：409 零写（status/version 均未动）
	stale := v1 + 9
	_, err = dsvc.SoftDelete(ctx, "verdel_item", doc.ID, "system", &stale)
	requireVersionConflict(t, err, stale, v1)
	got, err := dsvc.Get(ctx, "verdel_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, got.Status)
	require.EqualValues(t, v1, got.Version)

	// 版本相等才迁移：active→deleted（迁移分配新 revision）
	del, err := dsvc.SoftDelete(ctx, "verdel_item", doc.ID, "system", &v1)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, del.Status)
	require.Greater(t, del.Version, v1)
	v2 := del.Version

	// 幂等重复软删：目标态相同，版本不校验（陈旧版本同样放行返回现态）
	again, err := dsvc.SoftDelete(ctx, "verdel_item", doc.ID, "system", &stale)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, again.Status)
	require.EqualValues(t, v2, again.Version)

	// stale 恢复：409 零写（行仍 deleted、版本不变）
	_, err = dsvc.Restore(ctx, "verdel_item", doc.ID, "system", &stale)
	requireVersionConflict(t, err, stale, v2)
	got, err = dsvc.Get(ctx, "verdel_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, got.Status)
	require.EqualValues(t, v2, got.Version)

	// 版本相等才迁移：deleted→active
	res, err := dsvc.Restore(ctx, "verdel_item", doc.ID, "system", &v2)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res.Status)
	require.Greater(t, res.Version, v2)
	v3 := res.Version

	// 幂等重复恢复：active 行带任意版本返回现态
	res2, err := dsvc.Restore(ctx, "verdel_item", doc.ID, "system", &stale)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res2.Status)
	require.EqualValues(t, v3, res2.Version)

	// 旧契约：无版本（nil）无条件迁移（既有调用方零改动语义）
	del2, err := dsvc.SoftDelete(ctx, "verdel_item", doc.ID, "system", nil)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, del2.Status)
	res3, err := dsvc.Restore(ctx, "verdel_item", doc.ID, "system", nil)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res3.Status)

	// 行不存在同样 404（版本参数不改变定位语义）
	_, err = dsvc.SoftDelete(ctx, "verdel_item", 999999, "system", &stale)
	require.Equal(t, "DATA_NOT_FOUND", mustAE(t, err).Code)
	_, err = dsvc.Restore(ctx, "verdel_item", 999999, "system", &stale)
	require.Equal(t, "DATA_NOT_FOUND", mustAE(t, err).Code)
}

// TestRestore_VersionGuardBeforeUnique 版本拦截先于唯一键查重：版本不匹配 409 零写，
// 不触发查重；版本相等才进入查重（恢复唯一约束语义保持不变）。
func TestRestore_VersionGuardBeforeUnique(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	const name = "veruniq_item"

	_, err := tsvc.Register(ctx, service.RegisterInput{
		TypeName:     name,
		Fields:       []meta.Field{{Name: "code", Type: "string", Required: true}},
		UniqueFields: []string{"code"},
	}, "system")
	require.NoError(t, err)

	b, err := insertErr(t, dsvc, name, map[string]any{"code": "coll"})
	require.NoError(t, err)
	// 旧契约软删 b，随后同键插入 a（软删行不占用唯一性 → 放行）
	del, err := dsvc.SoftDelete(ctx, name, b.ID, "system", nil)
	require.NoError(t, err)
	bv := del.Version
	a, err := insertErr(t, dsvc, name, map[string]any{"code": "coll"})
	require.NoError(t, err)

	// stale 恢复：版本 409（expected_version/current_version 坐标），无 existing_id——
	// 未走到查重；行保持 deleted 零写
	stale := bv + 9
	_, err = dsvc.Restore(ctx, name, b.ID, "system", &stale)
	requireVersionConflict(t, err, stale, bv)
	got, err := dsvc.Get(ctx, name, b.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, got.Status)
	require.EqualValues(t, bv, got.Version)

	// 版本相等：进入查重 → 唯一键冲突 409（existing_id=a.ID），零写仍 deleted
	_, err = dsvc.Restore(ctx, name, b.ID, "system", &bv)
	e := mustAE(t, err)
	require.Equal(t, 409, e.HTTP)
	require.Equal(t, apperr.CodeConflict, e.Code)
	require.EqualValues(t, a.ID, e.Detail["existing_id"])
	got, err = dsvc.Get(ctx, name, b.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, got.Status)
	require.EqualValues(t, bv, got.Version)

	// 冲突解除（软删 a）后版本相等的恢复成功
	_, err = dsvc.SoftDelete(ctx, name, a.ID, "system", nil)
	require.NoError(t, err)
	res, err := dsvc.Restore(ctx, name, b.ID, "system", &bv)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res.Status)
}

// TestImport_ReuseIDVersionNoRepeat full replace 导入重用 id 时 revision 不重复：
// 重灌分配的新版本严格大于该 id 曾消费过的全部版本，导入前持有的版本全部失效。
func TestImport_ReuseIDVersionNoRepeat(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "vreuse_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "vreuse_item", 1, "a")
	issued := map[int64]bool{doc.Version: true}
	upd, err := dsvc.Update(ctx, "vreuse_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: doc.Version}, "system")
	require.NoError(t, err)
	issued[upd.Version] = true
	del, err := dsvc.SoftDelete(ctx, "vreuse_item", doc.ID, "system", nil)
	require.NoError(t, err)
	issued[del.Version] = true

	// 全量替换重导同一文件：id=1 重用，version 分配新 revision
	file, err := json.Marshal(exportAll(t, dsvc, "vreuse_item"))
	require.NoError(t, err)
	importRaw(t, dsvc, "vreuse_item", file)
	row, err := dsvc.Get(ctx, "vreuse_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, row.Status)
	require.False(t, issued[row.Version], "导入重用 id 不得复用该 id 曾用过的版本")
	require.Greater(t, row.Version, del.Version)

	// 导入前持有的版本已失效：stale 恢复 409；当前版本恢复成功
	stale := del.Version
	_, err = dsvc.Restore(ctx, "vreuse_item", doc.ID, "system", &stale)
	requireVersionConflict(t, err, stale, row.Version)
	res, err := dsvc.Restore(ctx, "vreuse_item", doc.ID, "system", &row.Version)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res.Status)
}

// TestSoftDeleteRestore_HTTPVersionGuard HTTP e2e：DELETE 空 body 与 {} 维持旧契约
// （无条件迁移）；带 version 的 200/409/422 分档；restore body 可选 version。
func TestSoftDeleteRestore_HTTPVersionGuard(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()
	gin.SetMode(gin.TestMode)
	r := handler.New(handler.Deps{Types: tsvc, Data: dsvc})

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "hxdel_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "hxdel_item", 1, "a")
	id := strconv.FormatInt(doc.ID, 10)

	del := func(body string) (int, string, repository.Document) {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(http.MethodPost, "/api/v1/data/hxdel_item/"+id+"/delete", nil)
		} else {
			req = httptest.NewRequest(http.MethodPost, "/api/v1/data/hxdel_item/"+id+"/delete",
				strings.NewReader(body))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var env struct {
			Code int                 `json:"code"`
			Data repository.Document `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
		return w.Code, w.Body.String(), env.Data
	}
	restore := func(body string) (int, string, repository.Document) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/restore",
			strings.NewReader(body)))
		var env struct {
			Code int                 `json:"code"`
			Data repository.Document `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
		return w.Code, w.Body.String(), env.Data
	}

	// {"version":陈旧} 删除 → 409（100008），行不动（active 行上版本保护实际拦迁移）
	code, body, _ := del(`{"version":99}`)
	require.Equal(t, http.StatusConflict, code, body)
	require.Contains(t, body, `"code":100008`)

	// 空 body = 旧契约：无条件迁移
	code, body, data := del("")
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusDeleted, data.Status)
	cur := data.Version

	// 目标态相同的重复请求幂等（版本判断仅约束实际迁移）：已删行带陈旧 version
	// → 200 返回现态，版本不再递增
	code, body, data = del(`{"version":99}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusDeleted, data.Status)
	require.EqualValues(t, cur, data.Version)

	// restore 无 version = 旧契约：无条件恢复
	code, body, data = restore(`{"type_name":"hxdel_item","id":"` + id + `"}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusActive, data.Status)
	cur = data.Version

	// {"version":匹配} 删除 → 200 deleted
	code, body, data = del(`{"version":` + strconv.FormatInt(cur, 10) + `}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusDeleted, data.Status)
	cur = data.Version

	// restore 带陈旧 version → 409（deleted→active 实际迁移被拦）
	code, body, _ = restore(`{"type_name":"hxdel_item","id":"` + id + `","version":99}`)
	require.Equal(t, http.StatusConflict, code, body)
	require.Contains(t, body, `"code":100008`)

	// restore 带匹配 version → 200 active
	code, body, data = restore(`{"type_name":"hxdel_item","id":"` + id + `","version":` + strconv.FormatInt(cur, 10) + `}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusActive, data.Status)

	// {} 维持旧契约：无条件迁移
	code, body, data = del(`{}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, repository.RowStatusDeleted, data.Status)

	// {"version":0} → 422（100000）
	code, body, _ = del(`{"version":0}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Contains(t, body, `"code":100000`)
}
