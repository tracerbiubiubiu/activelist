//go:build integration

// M-A1 迁移集成验证（真 PG）：首次执行建表、二次执行幂等（ErrNoChange 归一 nil）、
// 元数据两表与唯一索引在位（并发注册 409 的 DB 侧依据，A1）。
package repository_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxv5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
	"github.com/tracerbiubiubiu/activelist/migrations"
)

func startMigrationPG(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	container, err := postgres.Run(ctx,
		"postgres:18.6-trixie@sha256:e3d24f6906d0760bf4da0dae872415391ededefad23fd471c47dc53b0ddf3c42", // 与部署 PG18 基线一致，独立临时数据
		postgres.WithDatabase("al_mig_test"),
		postgres.WithUsername("al"),
		postgres.WithPassword("al_test"),
		// This fixture keeps one database process through the complete upgrade.
		testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql": "rw"}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
			wait.ForListeningPort("5432/tcp"),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		require.NoError(t, container.Terminate(cleanupCtx), "terminate isolated PostgreSQL")
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return ctx, dsn
}

func TestMigrateUp(t *testing.T) {
	ctx, dsn := startMigrationPG(t)

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

func TestMigrateB3PreservesLegacyDynamicTableNames(t *testing.T) {
	ctx, dsn := startMigrationPG(t)
	migrateToV2(t, dsn)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	types := service.NewTypeService(pool)
	data := service.NewDataService(pool, 20, 100, 100, 1<<20)
	legacyNames := []string{"al_b3_stream", "al_b3_outbox", "al_b3_operation_receipts"}
	definitions := make(map[string]*meta.Definition, len(legacyNames))
	documents := make(map[string]*repository.Document, len(legacyNames))
	for _, name := range legacyNames {
		definition, err := types.Register(ctx, service.RegisterInput{
			TypeName: name, Fields: []meta.Field{{Name: "label", Type: "string", Required: true}},
		}, "fixture")
		require.NoError(t, err)
		definitions[name] = definition
		document, err := data.Insert(ctx, name, service.InsertInput{Data: map[string]any{"label": "legacy " + name}}, "fixture")
		require.NoError(t, err)
		documents[name] = document
	}

	require.NoError(t, repository.MigrateUp(dsn))
	require.NoError(t, repository.MigrateUp(dsn), "upgrading is idempotent")
	var version int
	var dirty bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.Equal(t, 4, version)
	require.False(t, dirty)
	for _, name := range legacyNames {
		definition, err := types.Get(ctx, name)
		require.NoError(t, err)
		require.Equal(t, definitions[name], definition)
		document, err := data.Get(ctx, name, documents[name].ID)
		require.NoError(t, err)
		require.Equal(t, documents[name], document)

		var created bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`, "_"+name,
		).Scan(&created))
		require.True(t, created, "missing B3 infrastructure table for %s", name)
	}
}

func migrateToV2(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx/v5", dsn)
	require.NoError(t, err)
	defer db.Close()
	driver, err := pgxv5.WithInstance(db, &pgxv5.Config{})
	require.NoError(t, err)
	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	migrator, err := migrate.NewWithInstance("iofs", source, "activelist", driver)
	require.NoError(t, err)
	defer migrator.Close()
	require.NoError(t, migrator.Migrate(2))
}
