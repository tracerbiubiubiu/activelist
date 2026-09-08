// Package app 应用装配（providers——依赖构造函数；注入集合见 wire_gen.go 头注）。
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	utilslogger "github.com/tracerbiubiubiu/zhuzhao-utils/logger"
	utilspostgres "github.com/tracerbiubiubiu/zhuzhao-utils/postgres"

	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/handler"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// provideLogger 应用日志（utils logger：slog + lumberjack 轮转，JSON Lines 稳定字段）。
func provideLogger(cfg *config.Config) *slog.Logger {
	return utilslogger.New(utilslogger.Config{Level: cfg.Log.Level, Dir: cfg.Log.Dir})
}

// providePool PG 连接池（utils postgres：连接超时/缓存 describe 模式内置）+ 启动迁移。
// 迁移幂等且驱动带 advisory lock（A7 多副本安全）；失败拒绝启动（缺表的服务是假服务）。
func providePool(cfg *config.Config) (*pgxpool.Pool, func(), error) {
	pc := utilspostgres.Config{
		Host: cfg.Postgres.Host, Port: cfg.Postgres.Port,
		User: cfg.Postgres.User, Password: cfg.Postgres.Password,
		DBName:          cfg.Postgres.DBName,
		MaxOpenConns:    cfg.Postgres.MaxOpenConns,
		SSLMode:         "disable",
		ApplicationName: "activelist",
	}
	pool, cleanup, err := utilspostgres.New(pc)
	if err != nil {
		return nil, nil, err
	}
	if err := repository.MigrateUp(cfg.Postgres.DSN()); err != nil {
		cleanup()
		return nil, nil, err
	}
	return pool, cleanup, nil
}

// provideReadyz 依赖就绪探针（检 PG 可查询）。
func provideReadyz(pool *pgxpool.Pool) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return pool.Ping(ctx)
	}
}

// provideEngine HTTP 路由引擎。
func provideEngine(types *service.TypeService, ready func() error) *gin.Engine {
	return handler.New(handler.Deps{Types: types, Ready: ready})
}
