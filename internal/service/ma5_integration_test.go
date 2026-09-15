//go:build integration

// M-A5 / A5 验收集成验证（真 PG）：导出含软删 → 清空 → 导入 → 数据一致（version
// 重置 1）/ 重导幂等 / 序列正确（后续插入不冲突）/ 导入期间并发写 5s 快速失败
// 409 / 校验失败整笔回滚 / 废弃类型拒导入 / HTTP e2e 往返。
package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

func exportAll(t *testing.T, dsvc *service.DataService, typeName string) []repository.Document {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, dsvc.Export(context.Background(), typeName, &buf))
	var docs []repository.Document
	require.NoError(t, json.Unmarshal(buf.Bytes(), &docs))
	return docs
}

func importRaw(t *testing.T, dsvc *service.DataService, typeName string, raw []byte) *service.ImportResult {
	t.Helper()
	res, err := dsvc.Import(context.Background(), typeName, bytes.NewReader(raw), "importer")
	require.NoError(t, err)
	return res
}

// A5 主链：导出（含软删）→ 清空 → 导入 → 数据一致（version 重置 1）+ 序列正确。
func TestA5_ExportImportRoundTrip(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "xfer_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	d1 := insertDoc(t, dsvc, "xfer_item", 1, "a")
	d2 := insertDoc(t, dsvc, "xfer_item", 2, "b")
	// 一行更新（version 2）+ 一行软删（导出须含）
	_, err = dsvc.Update(ctx, "xfer_item", d1.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(9)}, Version: 1}, "updater")
	require.NoError(t, err)
	_, err = dsvc.SoftDelete(ctx, "xfer_item", d2.ID, "system")
	require.NoError(t, err)

	exported := exportAll(t, dsvc, "xfer_item")
	require.Len(t, exported, 2) // 含软删行（2 行：1 更新 + 1 软删）
	hasDeleted := false
	for _, d := range exported {
		if d.Status == repository.StatusDeleted {
			hasDeleted = true
		}
	}
	require.True(t, hasDeleted, "导出必须含软删行")

	// 清空环境（模拟迁移目标库）
	raw, err := json.Marshal(exported)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM xfer_item`)
	require.NoError(t, err)

	res := importRaw(t, dsvc, "xfer_item", raw)
	require.EqualValues(t, 2, res.Rows)
	require.EqualValues(t, 0, res.Deleted) // 表已手工清空，导入侧清表计数自然为 0
	require.EqualValues(t, 2, res.MaxID)

	// 数据一致：id/status/data/操作者/时间戳全等，version 重置 1
	reexported := exportAll(t, dsvc, "xfer_item")
	require.Len(t, reexported, len(exported))
	byID := map[int64]repository.Document{}
	for _, d := range reexported {
		byID[d.ID] = d
		require.EqualValues(t, 1, d.Version, "导入 version 重置 1")
	}
	for _, want := range exported {
		got, ok := byID[want.ID]
		require.True(t, ok, "缺行 id=%d", want.ID)
		require.Equal(t, want.Status, got.Status)
		require.Equal(t, want.Data, got.Data)
		require.Equal(t, want.CreatedBy, got.CreatedBy)
		require.True(t, want.CreatedAt.Equal(got.CreatedAt), "created_at 须保留")
		require.True(t, want.UpdatedAt.Equal(got.UpdatedAt), "updated_at 须保留")
	}

	// 序列正确：后续插入不冲突（id = max+1）
	nd := insertDoc(t, dsvc, "xfer_item", 3, "seq")
	require.EqualValues(t, 3, nd.ID)

	// 软删状态经往返保留：id=2 仍不可见
	docs, err := dsvc.List(ctx, "xfer_item", nil, 100)
	require.NoError(t, err)
	require.Len(t, docs, 2) // 3 行中 1 行 deleted
}

// A5 幂等：同一文件重导结果一致（重导后再导出逐字段相等）。
func TestA5_ImportIdempotent(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "idem_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		insertDoc(t, dsvc, "idem_item", i+1, string(rune('a'+i)))
	}
	file, err := json.Marshal(exportAll(t, dsvc, "idem_item"))
	require.NoError(t, err)

	importRaw(t, dsvc, "idem_item", file)
	first := exportAll(t, dsvc, "idem_item")
	importRaw(t, dsvc, "idem_item", file) // 重导同一文件
	second := exportAll(t, dsvc, "idem_item")

	require.Len(t, first, len(second))
	for i := range first {
		require.Equal(t, first[i].ID, second[i].ID)
		require.Equal(t, first[i].Data, second[i].Data)
		require.Equal(t, first[i].Status, second[i].Status)
		require.EqualValues(t, 1, second[i].Version)
	}
}

// A5 校验与回滚：缺必填 / 重复 id / 非法 status / 缺 id → 422 且原数据不动。
func TestA5_ImportValidationRollback(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "va_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	keep := insertDoc(t, dsvc, "va_item", 1, "keep")

	cases := []struct {
		name  string
		file  string
		dupID int64 // >0 时断言 detail.duplicate_id（重复 id 用例的定位回显）
	}{
		{"缺必填字段", `[{"id":"10","data":{"qty":1}}]`, 0},
		{"文件内重复 id", `[{"id":"10","data":{"name":"x"}},{"id":"10","data":{"name":"y"}}]`, 10},
		{"非法 status", `[{"id":"10","status":"gone","data":{"name":"x"}}]`, 0},
		{"缺 id", `[{"data":{"name":"x"}}]`, 0},
		{"未知字段", `[{"id":"10","data":{"name":"x","typo":1}}]`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dsvc.Import(ctx, "va_item", strings.NewReader(tc.file), "importer")
			e := mustAE(t, err)
			require.Equal(t, 422, e.HTTP, tc.name)
			require.Equal(t, "VALIDATION_ERROR", e.Code, tc.name)
			if tc.dupID > 0 {
				require.Equal(t, tc.dupID, e.Detail["duplicate_id"], tc.name)
			}
			// 原数据未动（事务回滚）
			got, err := dsvc.Get(ctx, "va_item", keep.ID)
			require.NoError(t, err)
			require.Equal(t, "keep", got.Data["name"])
		})
	}
}

// A5/§7 导入期间并发写：锁表持有中，常规更新 5s lock_timeout 快速失败 409。
func TestA5_ImportConcurrentWriteFastFail(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "lock2_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	doc := insertDoc(t, dsvc, "lock2_item", 1, "a")

	// 模拟导入事务：持替换锁（等价于导入事务开头的 LOCK TABLE）
	tx1, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx1.Rollback(ctx) }()
	require.NoError(t, repository.LockForReplace(ctx, tx1, "lock2_item"))

	start := time.Now()
	_, err = dsvc.Update(ctx, "lock2_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "system")
	elapsed := time.Since(start)
	e := mustAE(t, err)
	require.Equal(t, 409, e.HTTP)
	require.Equal(t, "CONFLICT", e.Code)
	require.Less(t, elapsed, 10*time.Second, "应 5s lock_timeout 快速失败而非挂死")
	require.GreaterOrEqual(t, elapsed, 4*time.Second, "应等待至超时阈值")

	require.NoError(t, tx1.Rollback(ctx))
	// 释放后写恢复正常
	_, err = dsvc.Update(ctx, "lock2_item", doc.ID, service.UpdateInput{
		Data: map[string]any{"qty": float64(2)}, Version: 1}, "system")
	require.NoError(t, err)
}

// A5 边界：空数组 = 清空全表且序列重置；废弃类型拒导入。
func TestA5_ImportEmptyAndDeprecated(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "emp_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	insertDoc(t, dsvc, "emp_item", 1, "a")
	insertDoc(t, dsvc, "emp_item", 2, "b")

	res := importRaw(t, dsvc, "emp_item", []byte(`[]`))
	require.EqualValues(t, 0, res.Rows)
	require.EqualValues(t, 2, res.Deleted)
	require.EqualValues(t, 0, res.MaxID)

	docs, err := dsvc.List(ctx, "emp_item", nil, 100)
	require.NoError(t, err)
	require.Empty(t, docs)

	// 序列重置：新插入从 id=1 起
	nd := insertDoc(t, dsvc, "emp_item", 3, "c")
	require.EqualValues(t, 1, nd.ID)

	// 废弃类型拒导入
	_, err = tsvc.Deprecate(ctx, "emp_item", "system")
	require.NoError(t, err)
	_, err = dsvc.Import(ctx, "emp_item", strings.NewReader(`[{"id":"1","data":{"name":"x"}}]`), "importer")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "TYPE_DEPRECATED", mustAE(t, err).Code)
}

// A5 HTTP e2e：export 裸数组 + import 信封汇总 + 全链往返。
func TestA5_HTTPExportImport(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	gin.SetMode(gin.TestMode)
	r := handler.New(handler.Deps{Types: tsvc, Data: dsvc})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types",
		strings.NewReader(`{"type_name":"hx_item","fields":[{"name":"name","type":"string","required":true}]}`)))
	require.Equal(t, http.StatusOK, w.Code)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/hx_item",
		strings.NewReader(`{"data":{"name":"n1"}}`)))
	require.Equal(t, http.StatusOK, w.Code)

	// 导出 = 裸数组
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data/hx_item/export", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var docs []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &docs))
	require.Len(t, docs, 1)
	file, err := json.Marshal(docs)
	require.NoError(t, err)

	// 导入 = §6.8 信封 + 批次汇总
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/hx_item/import", bytes.NewReader(file)))
	require.Equal(t, http.StatusOK, w.Code)
	var env struct {
		Data struct {
			Rows    int64 `json:"rows"`
			Deleted int64 `json:"deleted"`
			MaxID   int64 `json:"max_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.EqualValues(t, 1, env.Data.Rows)
	require.EqualValues(t, 1, env.Data.Deleted)
	require.EqualValues(t, 1, env.Data.MaxID)

	// 非法文件 → 400/422 信封
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/hx_item/import",
		strings.NewReader(`{"not":"array"}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 未知类型导出 → 404（gate 在流式写出前，可安全转错误响应）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data/ghost_type/export", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

// 并发导入按类型互斥（§7：SHARE ROW EXCLUSIVE 与自身冲突）——两并发导入都成功
// 且终态一致（后提交者胜出，替换语义）。
func TestA5_ConcurrentImportsSerialized(t *testing.T) {
	pool, tsvc := setupPG(t)
	dsvc := newDataSvc(pool)
	ctx := context.Background()

	_, err := tsvc.Register(ctx, service.RegisterInput{TypeName: "par_item", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	fileA := []byte(`[{"id":"1","data":{"name":"A"}}]`)
	fileB := []byte(`[{"id":"2","data":{"name":"B"}}]`)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, file := range [][]byte{fileA, fileB} {
		wg.Add(1)
		go func(f []byte) {
			defer wg.Done()
			_, err := dsvc.Import(ctx, "par_item", bytes.NewReader(f), "importer")
			errs <- err
		}(file)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	docs := exportAll(t, dsvc, "par_item")
	require.Len(t, docs, 1) // 全量替换：终态只剩一份文件的内容
}
