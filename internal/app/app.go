// Package app 应用装配与生命周期（Wire DI 语义手工同步——对齐 taskrunner/zhuzhao
// 微服务结构基线；wire_gen.go 为唯一装配源，注入集合见该文件头注）。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/activelist/internal/config"
)

// App 应用实例：HTTP（/healthz /readyz；/api/v1 随 M-A2 起挂载）。
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	engine *gin.Engine
	pool   pgxPool
	server *http.Server
}

// pgxPool 最小接口（*pgxpool.Pool 满足；解耦 app↔pgx 具体类型，便于测试替身）。
type pgxPool interface {
	Ping(ctx context.Context) error
	Close()
}

func NewApp(cfg *config.Config, logger *slog.Logger, engine *gin.Engine, pool pgxPool) *App {
	return &App{cfg: cfg, logger: logger, engine: engine, pool: pool}
}

// Run 启动并阻塞至退出信号；优雅停止（SIGTERM 排空在途请求——compose 多副本
// 滚动重启单副本服务不中断，A7）。
func (a *App) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", a.cfg.Server.Port),
		Handler:      a.engine,
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		a.logger.Info("activelist serving", "addr", a.server.Addr)
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		a.logger.Error("server failed", "err", err)
		return err
	case <-ctx.Done():
		a.logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.server.Shutdown(shutdownCtx)
	return nil
}
