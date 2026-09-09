// 应用装配（Wire 语义手工同步——生成器依赖 github.com/google/wire 未入 go.sum，
// 暂以本文件为唯一装配源；如需可再生：go get github.com/google/wire@v0.7.0 后
// 依下方「注入集合」重建 wireinject 文件并运行 wire。taskrunner 同款）。
//
// 注入集合（InitializeApp）：
//	provideLogger / providePool（含启动迁移）/ provideTypeService / provideDataService /
//	provideReadyz / provideEngine → NewApp

//go:build !wireinject
// +build !wireinject

package app

import (
	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// InitializeApp 依赖装配入口。
func InitializeApp(cfg *config.Config) (*App, func(), error) {
	logger := provideLogger(cfg)
	pool, cleanupPool, err := providePool(cfg)
	if err != nil {
		return nil, nil, err
	}
	types := service.NewTypeService(pool)
	data := service.NewDataService(pool, cfg.Business.PageSizeDefault, cfg.Business.PageSizeMax, cfg.Business.ImportBatchRows)
	engine := provideEngine(types, data, provideReadyz(pool))
	return NewApp(cfg, logger, engine), func() { cleanupPool() }, nil
}
