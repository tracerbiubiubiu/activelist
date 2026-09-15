//go:build integration

// M-A3 集成验证（真 PG；A2 / A4 + A1 尾巴 deprecated 拒写接线）：
// CRUD 生命周期 / 软删语义（列表排除、单查可见、更新 409、重复软删幂等、恢复可写）/
// keyset 复合游标遍历 / 乐观锁（含 6 并发恰一成功）/ 废弃类型拒写 / HTTP 信封与分页钳制。
package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func newDataSvc(pool *pgxpool.Pool) *service.DataService {
	return service.NewDataService(pool, 20, 100, 500, 1<<30)
}

func insertDoc(t *testing.T, dsvc *service.DataService, typeName string, qty int, name string) *repository.Document {
	t.Helper()
	doc, err := dsvc.Insert(context.Background(), typeName, service.InsertInput{
		Data: map[string]any{"name": name, "qty": float64(qty), "tags": []any{"t" + name}},
	}, "system")
	require.NoError(t, err)
	return doc
}

// A2 主链：插入 → 单查 → 更新（合并/版本递增）→ 列表排序不变量 → keyset 遍历不重不漏。
func TestA2_CRUDLifecycle(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "inv_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)

	doc := insertDoc(t, dsvc, "inv_item", 1, "a")
	require.Positive(t, doc.ID)
	require.EqualValues(t, 1, doc.Version)
	require.Equal(t, repository.RowStatusActive, doc.Status)
	require.Equal(t, "system", doc.CreatedBy)
	require.Equal(t, "a", doc.Data["name"])
	require.Equal(t, []any{"ta"}, doc.Data["tags"])

	got, err := dsvc.Get(ctx, "inv_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, doc.ID, got.ID)
	require.Equal(t, "a", got.Data["name"])

	// 更新 = 部分覆盖合并：只改 qty，name/tags 保留；version 1→2
	upd, err := dsvc.Update(ctx, "inv_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "operator-1")
	require.NoError(t, err)
	require.EqualValues(t, 2, upd.Version)
	require.Equal(t, float64(2), upd.Data["qty"])
	require.Equal(t, "a", upd.Data["name"])
	require.Equal(t, "operator-1", upd.UpdatedBy)

	// 显式 null 清 optional 字段（tags 放行）；required 清值 422
	upd, err = dsvc.Update(ctx, "inv_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"tags": nil}, Version: 2}, "system")
	require.NoError(t, err)
	require.Nil(t, upd.Data["tags"])
	_, err = dsvc.Update(ctx, "inv_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"name": nil}, Version: 3}, "system")
	require.Equal(t, 422, mustAE(t, err).HTTP)

	// 列表排序不变量：created_at DESC, id DESC（同微秒 tie 由 id 决断）
	for i := 0; i < 4; i++ {
		insertDoc(t, dsvc, "inv_item", i+10, fmt.Sprintf("n%d", i))
	}
	docs, err := dsvc.List(ctx, "inv_item", nil, 100)
	require.NoError(t, err)
	require.Len(t, docs, 5)
	for i := 1; i < len(docs); i++ {
		a, b := docs[i-1], docs[i]
		require.True(t, a.CreatedAt.After(b.CreatedAt) ||
			(a.CreatedAt.Equal(b.CreatedAt) && a.ID > b.ID), "排序不变量被破坏")
	}

	// keyset 遍历：page_size=2 走完全量，不重不漏
	var walked []int64
	var cur *repository.Cursor
	for {
		page, err := dsvc.List(ctx, "inv_item", cur, 2)
		require.NoError(t, err)
		for _, d := range page {
			walked = append(walked, d.ID)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		cur = &repository.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	require.Len(t, walked, 5)
	seen := map[int64]bool{}
	for _, id := range walked {
		require.False(t, seen[id], "遍历重复 id %d", id)
		seen[id] = true
	}
	require.Equal(t, docs[0].ID, walked[0], "首页应从最新行开始")
}

// A2 软删语义：列表排除、单查可见、更新 409、重复软删幂等、恢复后可写。
func TestA2_SoftDeleteFlow(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "soft_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "soft_item", 1, "a")

	del, err := dsvc.SoftDelete(ctx, "soft_item", doc.ID, "system")
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, del.Status)
	require.EqualValues(t, 2, del.Version) // 状态迁移同样递增版本（旧文档立即过期）

	// 列表排除软删行
	docs, err := dsvc.List(ctx, "soft_item", nil, 100)
	require.NoError(t, err)
	require.Empty(t, docs)

	// 单查可见（审计对账与恢复入口）
	got, err := dsvc.Get(ctx, "soft_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, got.Status)

	// 软删行更新 409
	_, err = dsvc.Update(ctx, "soft_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(9)}, Version: del.Version}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "CONFLICT", mustAE(t, err).Code)

	// 重复软删幂等：现态返回、版本不再递增
	again, err := dsvc.SoftDelete(ctx, "soft_item", doc.ID, "system")
	require.NoError(t, err)
	require.Equal(t, del.Version, again.Version)
	require.Equal(t, repository.RowStatusDeleted, again.Status)

	// 恢复 → active、版本 +1、可写
	res, err := dsvc.Restore(ctx, "soft_item", doc.ID, "system")
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusActive, res.Status)
	require.EqualValues(t, 3, res.Version)

	upd, err := dsvc.Update(ctx, "soft_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(5)}, Version: 3}, "system")
	require.NoError(t, err)
	require.Equal(t, float64(5), upd.Data["qty"])

	// 重复恢复幂等：active 行直接返回现态——版本已随中间的更新递增，不再变动
	res2, err := dsvc.Restore(ctx, "soft_item", doc.ID, "system")
	require.NoError(t, err)
	require.Equal(t, upd.Version, res2.Version)
	require.Equal(t, repository.RowStatusActive, res2.Status)
}

// A2/A4 负向：数据行不存在 404（DATA_NOT_FOUND）；类型不存在 404（TYPE_NOT_FOUND）。
func TestA2_DataNotFound(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "nf_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)

	_, err = dsvc.Get(ctx, "nf_item", 999999)
	nf := mustAE(t, err)
	require.Equal(t, 404, nf.HTTP)
	require.Equal(t, "DATA_NOT_FOUND", nf.Code)
	require.Equal(t, "nf_item", nf.Detail["type_name"]) // 404 回显定位坐标
	require.EqualValues(t, 999999, nf.Detail["id"])

	_, err = dsvc.Update(ctx, "nf_item", 999999, service.UpdateInput{Version: 1}, "system")
	require.Equal(t, "DATA_NOT_FOUND", mustAE(t, err).Code)

	_, err = dsvc.SoftDelete(ctx, "nf_item", 999999, "system")
	require.Equal(t, "DATA_NOT_FOUND", mustAE(t, err).Code)

	_, err = dsvc.Restore(ctx, "nf_item", 999999, "system")
	require.Equal(t, "DATA_NOT_FOUND", mustAE(t, err).Code)

	_, err = dsvc.Get(ctx, "ghost_type", 1)
	require.Equal(t, "TYPE_NOT_FOUND", mustAE(t, err).Code)
}

// apperrHolder 并发收集错误（require 非 goroutine 安全，收拢后主 goroutine 断言）。
type apperrHolder struct{ err error }

// A4 乐观锁：版本不匹配 409；过期版本复用 409；6 并发同版本更新恰一成功。
func TestA4_OptimisticLock(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "lock_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "lock_item", 1, "a")

	// 版本不匹配 409
	_, err = dsvc.Update(ctx, "lock_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 99}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "CONFLICT", mustAE(t, err).Code)

	// 成功更新后旧版本复用 409（读-改-写窗口外移给调用方的典型形态）
	_, err = dsvc.Update(ctx, "lock_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "system")
	require.NoError(t, err)
	_, err = dsvc.Update(ctx, "lock_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(3)}, Version: 1}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)

	// 6 并发同版本：FOR UPDATE 串行化 + version 校验 → 恰一 200，其余 409
	const workers = 6
	results := make(chan *apperrHolder, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := dsvc.Update(ctx, "lock_item", doc.ID, service.UpdateInput{
				Data:    map[string]any{"qty": float64(100 + n)},
				Version: 2,
			}, fmt.Sprintf("op-%d", n))
			results <- &apperrHolder{err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	okCount, conflictCount := 0, 0
	for h := range results {
		if h.err == nil {
			okCount++
			continue
		}
		e := mustAE(t, h.err)
		require.Equal(t, 409, e.HTTP)
		require.Equal(t, "CONFLICT", e.Code)
		conflictCount++
	}
	require.Equal(t, 1, okCount)
	require.Equal(t, workers-1, conflictCount)

	final, err := dsvc.Get(ctx, "lock_item", doc.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, final.Version)
}

// A1 尾巴：deprecated 类型拒绝写入（插入/更新 409 TYPE_DEPRECATED）；
// 读与生命周期操作（软删/恢复）放行——存量数据清理不被堵。
func TestA1_DeprecatedTypeRejectsWrite(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "legacy_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "legacy_item", 1, "a")

	_, err = tsvc.Deprecate(ctx, "legacy_item", "system")
	require.NoError(t, err)

	_, err = dsvc.Insert(ctx, "legacy_item", service.InsertInput{
		Data: map[string]any{"name": "b"}}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "TYPE_DEPRECATED", mustAE(t, err).Code)

	_, err = dsvc.Update(ctx, "legacy_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "TYPE_DEPRECATED", mustAE(t, err).Code)

	// 读放行
	got, err := dsvc.Get(ctx, "legacy_item", doc.ID)
	require.NoError(t, err)
	require.Equal(t, doc.ID, got.ID)
	// 生命周期操作放行
	del, err := dsvc.SoftDelete(ctx, "legacy_item", doc.ID, "system")
	require.NoError(t, err)
	require.Equal(t, repository.RowStatusDeleted, del.Status)
}

// HTTP 层 e2e：信封形状 / 游标成对 400 / 分页钳制回显 / 全链软删-恢复。
func TestA3_HTTPEnvelopeData(t *testing.T) {
	pool, tsvc := setupPG(t)
	// 故意用小 max（3）验证 page_size 钳制回显
	dsvc := service.NewDataService(pool, 2, 3, 500, 1<<30)
	gin.SetMode(gin.TestMode)
	r := handler.New(handler.Deps{Types: tsvc, Data: dsvc})

	w := httptest.NewRecorder()
	regBody := `{"type_name":"e2e_items","fields":[{"name":"name","type":"string","required":true},{"name":"qty","type":"int"}]}`
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types", strings.NewReader(regBody)))
	require.Equal(t, http.StatusOK, w.Code)

	// 插入 200 + 信封
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/e2e_items",
		strings.NewReader(`{"data":{"name":"n1","qty":1}}`)))
	require.Equal(t, http.StatusOK, w.Code)
	var env struct {
		Code int `json:"code"`
		Data struct {
			ID      int64  `json:"id,string"`
			Version int64  `json:"version"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	require.EqualValues(t, 1, env.Data.Version)
	require.Equal(t, "active", env.Data.Status)
	id := env.Data.ID

	// data 非 JSON 对象 → 400
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/e2e_items",
		strings.NewReader(`{"data":42}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 游标成对性 → 400（handler 层 BadRequest，通用段 10001）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/data/e2e_items?after_created_at=2026-09-08T00:00:00Z", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), `"code":10001`)

	// page_size 超限钳制回显（max=3）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data/e2e_items?page_size=999", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var listEnv struct {
		Data struct {
			PageSize   int `json:"page_size"`
			NextCursor any `json:"next_cursor"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listEnv))
	require.Equal(t, 3, listEnv.Data.PageSize)

	// 路径 id 非整数 → 400
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data/e2e_items/abc", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 乐观锁 409 经信封透出
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/data/e2e_items/%d/update", id),
		strings.NewReader(`{"data":{"qty":9},"version":77}`)))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), `"code":100008`)

	// 软删 → 单查带 deleted → 恢复 → active
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/data/e2e_items/%d/delete", id), nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"deleted"`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/data/e2e_items/%d", id), nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"deleted"`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/data/e2e_items/%d/restore", id), nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"active"`)

	// 未知类型 404
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data/ghost_type/1", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), `"code":100002`)
}
