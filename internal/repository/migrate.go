// 启动迁移执行（golang-migrate + postgres/pgx 驱动）。
// A7：迁移全库只执行一次——pgx 驱动自带会话级 advisory lock，多副本并发启动
// 串行化且幂等（已到最新版 = ErrNoChange 视为成功）。生产如走 init 容器/CI 执行，
// 可将本调用改为显式开关；当前开发/单机形态随服务启动执行。
package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxv5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 驱动注册 "pgx/v5"

	"github.com/tracerbiubiubiu/activelist/migrations"
)

// MigrateUp 执行全部 up 迁移。dsn 为 postgres:// URL（stdlib 与 pgx 驱动均接受）。
func MigrateUp(dsn string) error {
	sqlDB, err := sql.Open("pgx/v5", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer sqlDB.Close()

	driver, err := pgxv5.WithInstance(sqlDB, &pgxv5.Config{})
	if err != nil {
		return fmt.Errorf("migrate: driver: %w", err)
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate: source: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "activelist", driver)
	if err != nil {
		return fmt.Errorf("migrate: instance: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}
