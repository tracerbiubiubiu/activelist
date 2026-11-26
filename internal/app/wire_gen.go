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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/service"
)

// InitializeApp 依赖装配入口。
func InitializeApp(cfg *config.Config) (*App, func(), error) {
	// M-A6 fail-closed（启动级）：空验签密钥环或含空 SK 条目均拒绝启动（对齐
	// taskrunner C2，验收 A8；handler 层空环为请求级 401——双保险的启动半边）。
	// 注意两形态都必须在此拦：config.yaml 的 ${VAR:-} 在 env 未注入时展开为空值，
	// viper/mapstructure 会把该条目整个丢掉（环变空 map），config 层不可见。
	if len(cfg.Security.Callers) == 0 {
		return nil, nil, errors.New("security.callers 为空——拒绝启动（M-A6 fail-closed；配置 ACTIVELIST_CALLER_ZHUZHAO_SK）")
	}
	for ak, sk := range cfg.Security.Callers {
		if sk == "" {
			return nil, nil, fmt.Errorf("security.callers.%s 的 SK 为空——拒绝启动（fail-closed；配置 ACTIVELIST_CALLER_ZHUZHAO_SK）", ak)
		}
	}
	if cfg.B3.TypeName != "" {
		producerSK, coreSK := cfg.Security.Callers[cfg.B3.ProducerAK], cfg.Security.Callers[cfg.B3.CoreAK]
		if producerSK == "" || coreSK == "" || producerSK == coreSK || cfg.B3.ProducerAK == "zhuzhao" || cfg.B3.CoreAK == "zhuzhao" {
			return nil, nil, errors.New("ActiveList B3 requires distinct registered producer and Core command keys")
		}
		for ak, sk := range cfg.Security.Callers {
			if ak != cfg.B3.ProducerAK && ak != cfg.B3.CoreAK && (sk == producerSK || sk == coreSK) {
				return nil, nil, errors.New("ActiveList B3 keys cannot alias an ordinary caller")
			}
		}
	}
	logger := provideLogger(cfg)
	pool, cleanupPool, err := providePool(cfg)
	if err != nil {
		return nil, nil, err
	}
	types := service.NewTypeService(pool)
	data := service.NewDataService(pool, cfg.Business.PageSizeDefault, cfg.Business.PageSizeMax, cfg.Business.ImportBatchRows, cfg.Business.ImportMaxBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := data.EnableB3(ctx, cfg.B3); err != nil {
		cleanupPool()
		return nil, nil, err
	}
	engine := provideEngine(cfg, logger, types, data, provideReadyz(pool))
	app := NewApp(cfg, logger, engine)
	if cfg.B3.TypeName != "" {
		app.pushChanges = func(ctx context.Context) { data.RunB3Push(ctx, logger) }
	}
	return app, func() { cleanupPool() }, nil
}
