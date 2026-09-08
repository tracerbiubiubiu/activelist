//go:build integration

// M-A1 迁移集成验证（真 PG）：首次执行建表、二次执行幂等（ErrNoChange 归一 nil）、
// 元数据两表与唯一索引在位（并发注册 409 的 DB 侧依据，A1）。
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/tracerbiubiubiu/activelist/internal/repository"
)

func TestMigrateUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx,
		"postgres:15-alpine", // 本地已缓存（zhuzhao 同款；16-alpine 拉取受网络限制）
		postgres.WithDatabase("al_mig_test"),
		postgres.WithUsername("al"),
		postgres.WithPassword("al_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	require.NoError(t, repository.MigrateUp(dsn), "首次执行")
	require.NoError(t, repository.MigrateUp(dsn), "二次执行幂等")

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('data_types','data_type_schema_history')`).Scan(&n))
	require.Equal(t, 2, n)

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'uq_data_types_name'`).Scan(&n))
	require.Equal(t, 1, n)
}
