//go:build integration

// M-A2 / A1 验收集成验证（真 PG）：注册→建表→元数据落库→变更历史；
// 重复注册 409；typeName/字段非法 422（VALIDATION_ERROR / RESERVED_FIELD 分档）；
// 废弃流（幂等）；不存在 404；HTTP 信封 {code,msg,data,detail.error_code}（§6.8）。
package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// mustAE 断言错误为 *apperr.Error 并返回（业务错误契约检查）。
func mustAE(t *testing.T, err error) *apperr.Error {
	t.Helper()
	require.Error(t, err)
	e, ok := err.(*apperr.Error)
	require.True(t, ok, "应为 *apperr.Error: %v", err)
	return e
}

func setupPG(t *testing.T) (*pgxpool.Pool, *service.TypeService) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgres.Run(ctx,
		"postgres:15-alpine", // 本地已缓存（zhuzhao 同款）
		postgres.WithDatabase("al_a1_test"),
		postgres.WithUsername("al"),
		postgres.WithPassword("al_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, repository.MigrateUp(dsn))

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, service.NewTypeService(pool)
}

func sampleFields() []meta.Field {
	return []meta.Field{
		{Name: "name", Type: "string", Required: true},
		{Name: "qty", Type: "int"},
		{Name: "tags", Type: "string_list"},
	}
}

// A1 主链：注册 → 元数据 + 动态表 + 变更历史；重复注册 409。
func TestA1_RegisterLifecycle(t *testing.T) {
	pool, svc := setupPG(t)
	ctx := context.Background()

	def, err := svc.Register(ctx, service.RegisterInput{TypeName: "asset_inventory", Fields: sampleFields()}, "system")
	require.NoError(t, err)
	require.Equal(t, "asset_inventory", def.TypeName)
	require.Equal(t, "active", def.Status)
	require.EqualValues(t, 1, def.Version)
	require.Len(t, def.Fields, 3)

	// 元数据落库
	var status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM data_types WHERE type_name='asset_inventory'`).Scan(&status))
	require.Equal(t, "active", status)

	// 动态表保留列族齐全 + keyset 索引在位
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='asset_inventory' AND column_name IN
		 ('id','version','status','data','created_by','updated_by','created_at','updated_at')`).Scan(&n))
	require.Equal(t, 8, n)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE indexname='idx_asset_inventory_created'`).Scan(&n))
	require.Equal(t, 1, n)

	// 变更历史（op=register）
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM data_type_schema_history
		 WHERE type_name='asset_inventory' AND op='register'`).Scan(&n))
	require.Equal(t, 1, n)

	// 重复注册 → 409 TYPE_ALREADY_EXISTS
	_, err = svc.Register(ctx, service.RegisterInput{
		TypeName: "asset_inventory",
		Fields:   []meta.Field{{Name: "x", Type: "int"}}}, "system")
	require.Equal(t, 409, mustAE(t, err).HTTP)
	require.Equal(t, "TYPE_ALREADY_EXISTS", mustAE(t, err).Code)
}

// A1 负向：非法输入 422 分档；未知类型 404。
func TestA1_RegisterNegative(t *testing.T) {
	_, svc := setupPG(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, service.RegisterInput{
		TypeName: "BadName",
		Fields:   []meta.Field{{Name: "x", Type: "int"}}}, "system")
	require.Equal(t, 422, mustAE(t, err).HTTP)
	require.Equal(t, "VALIDATION_ERROR", mustAE(t, err).Code)

	_, err = svc.Register(ctx, service.RegisterInput{
		TypeName: "goods",
		Fields:   []meta.Field{{Name: "id", Type: "int"}}}, "system")
	require.Equal(t, 422, mustAE(t, err).HTTP)
	require.Equal(t, "RESERVED_FIELD", mustAE(t, err).Code)

	_, err = svc.Get(ctx, "ghost_type")
	require.Equal(t, 404, mustAE(t, err).HTTP)
	require.Equal(t, "TYPE_NOT_FOUND", mustAE(t, err).Code)
}

// 废弃流：active→deprecated 落历史；重复废弃幂等；不存在 404。
// （"deprecated 拒绝写入"的数据路径接线已于 M-A3 落地：TestA1_DeprecatedTypeRejectsWrite。）
func TestA1_DeprecateFlow(t *testing.T) {
	pool, svc := setupPG(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, service.RegisterInput{
		TypeName: "old_kind",
		Fields:   []meta.Field{{Name: "name", Type: "string"}}}, "system")
	require.NoError(t, err)

	def, err := svc.Deprecate(ctx, "old_kind", "system")
	require.NoError(t, err)
	require.Equal(t, "deprecated", def.Status)

	def, err = svc.Deprecate(ctx, "old_kind", "system") // 幂等
	require.NoError(t, err)
	require.Equal(t, "deprecated", def.Status)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM data_type_schema_history WHERE type_name='old_kind'`).Scan(&n))
	require.Equal(t, 2, n) // register + deprecate

	_, err = svc.Deprecate(ctx, "ghost_type", "system")
	require.Equal(t, 404, mustAE(t, err).HTTP)
}

// operator 空串兜底（COALESCE 回退列默认 'system'）：显式 NULL 不触发列 DEFAULT，
// 会 23502——M-A6 中间件上线后 X-Operator 缺头即此路径，回归网必须兜住。
func TestA1_EmptyOperatorFallback(t *testing.T) {
	pool, svc := setupPG(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, service.RegisterInput{
		TypeName: "anon_kind", Fields: []meta.Field{{Name: "name", Type: "string"}}}, "")
	require.NoError(t, err)

	var createdBy, changedBy string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT created_by FROM data_types WHERE type_name='anon_kind'`).Scan(&createdBy))
	require.Equal(t, "system", createdBy)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT changed_by FROM data_type_schema_history
		 WHERE type_name='anon_kind' AND op='register'`).Scan(&changedBy))
	require.Equal(t, "system", changedBy)

	// 废弃同路径（UPDATE 的 COALESCE 分支）
	_, err = svc.Deprecate(ctx, "anon_kind", "")
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT updated_by FROM data_types WHERE type_name='anon_kind'`).Scan(&createdBy))
	require.Equal(t, "system", createdBy)
}

// HTTP 层 e2e：路由 + §6.8 信封（201/409/422/400 形状断言）。
func TestA1_HTTPEnvelope(t *testing.T) {
	_, svc := setupPG(t)
	gin.SetMode(gin.TestMode)
	r := handler.New(handler.Deps{Types: svc})

	body := `{"type_name":"e2e_kind","fields":[{"name":"name","type":"string","required":true},{"name":"qty","type":"int"}]}`

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"code":0`)
	require.Contains(t, w.Body.String(), `"type_name":"e2e_kind"`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types", strings.NewReader(body)))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), `"code":100003`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types",
		strings.NewReader(`{"type_name":"Bad","fields":[{"name":"x","type":"int"}]}`)))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), `"code":100000`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/types/e2e_kind", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	require.Equal(t, "success", env.Message)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/types", strings.NewReader(`{bad`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// C7 回归（Deprecate×Evolve 交错）：Deprecate 的 GetByName 快照读发生在行锁等待
// 之前——并发演进提交后，历史行若用旧快照会记录演进前 schema（审计失真）。
// 修复 = moved=true 后（已持有行锁）锁内重读 fields 再写历史。
func TestDeprecateDuringEvolve_RecordsLockTimeSchema(t *testing.T) {
	pool, svc := setupPG(t)
	ctx := context.Background()

	_, err := svc.Register(ctx, service.RegisterInput{
		TypeName: "tmp_dep_evolve",
		Fields:   []meta.Field{{Name: "a", Type: "string"}},
	}, "t1")
	require.NoError(t, err)

	// T1：手工连接模拟进行中的 Evolve（持有行锁、新 schema_def 未提交）
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, `BEGIN`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx,
		`UPDATE data_types SET schema_def=$2, version=version+1 WHERE type_name=$1 AND status='active'`,
		"tmp_dep_evolve", `{"fields":[{"name":"a","type":"string"},{"name":"b","type":"int"}]}`)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, derr := svc.Deprecate(ctx, "tmp_dep_evolve", "t2")
		done <- derr
	}()
	time.Sleep(300 * time.Millisecond) // 让 T2 完成快照读并阻塞在行锁上
	_, err = conn.Exec(ctx, `COMMIT`)  // T1 提交演进，释放行锁
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Deprecate 未在 T1 提交后完成（意外悬挂）")
	}

	var histSchema string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT schema_def FROM data_type_schema_history WHERE type_name=$1 AND op='deprecate'`,
		"tmp_dep_evolve").Scan(&histSchema))
	require.Contains(t, histSchema, `"b"`,
		"deprecate 历史行应记录废弃时刻真实 schema（含演进新增的字段 b）——锁内重读生效")
}
