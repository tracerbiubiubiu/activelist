package app

// A8 fail-closed 启动级校验：空密钥环 / 空 SK 条目均拒绝初始化。
// 校验位于资源创建（logger/pool）之前，可无环境直接单测。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/config"
)

func TestInitializeApp_SecurityRingFailClosed(t *testing.T) {
	t.Run("空环拒绝", func(t *testing.T) {
		_, _, err := InitializeApp(&config.Config{})
		require.ErrorContains(t, err, "security.callers 为空")
	})
	t.Run("空 SK 条目拒绝", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Security.Callers = map[string]string{"zhuzhao": ""}
		_, _, err := InitializeApp(cfg)
		require.ErrorContains(t, err, "SK 为空")
	})
	// 「正常 SK 通过启动校验」不设单测：会真实连接 PG（providePool 含启动迁移），
	// 单测不应有环境副作用；正向由集成测试的整舱启动覆盖。
}
