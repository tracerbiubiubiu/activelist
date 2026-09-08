// activelist 入口（薄——装配走 internal/app Wire 语义，结构对齐 taskrunner/zhuzhao）。
//
// 用法：
//
//	apiserver serve [config.yaml]   # 常驻服务（默认读 config/config.yaml；纯 env 亦可）
package main

import (
	"log"
	"os"

	"github.com/tracerbiubiubiu/activelist/internal/app"
	"github.com/tracerbiubiubiu/activelist/internal/config"
)

func main() {
	args := os.Args[1:]
	path := "config/config.yaml"
	if len(args) > 1 {
		path = args[1]
	}
	if len(args) > 0 && args[0] != "serve" {
		os.Stderr.WriteString("用法: apiserver [serve [config.yaml]]\n")
		os.Exit(2)
	}

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	a, cleanup, err := app.InitializeApp(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	defer cleanup()
	if err := a.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}
