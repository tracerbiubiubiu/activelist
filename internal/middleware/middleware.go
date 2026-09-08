// middleware 中间件（对齐 taskrunner；M-A6 再补访问日志统一出口与 AK-SK 验签）。
package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// RequestID 读入站 X-Request-ID（zhuzhao 网关转发必带），缺省自生成并回显
// 响应头；zhuzhao 审计行与 activelist 访问日志以此跨查（ADR-003 审计落点机制）。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = "req-" + randomHex(16)
		}
		c.Set("request_id", rid)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
