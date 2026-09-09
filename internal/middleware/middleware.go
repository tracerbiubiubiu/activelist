// middleware 中间件：RequestID / AccessLog（M-A6 统一访问日志出口）/
// AKSKAuth + Operator（M-A6 服务间验签，16 号 §9 服务鉴权基线）。
package middleware

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"
	"github.com/tracerbiubiubiu/zhuzhao-utils/response"
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

// AKSKAuth 服务间 AK/SK HMAC 验签（M-A6；utils aksk canonical 覆盖
// X-Request-ID / X-Operator——「明文 X-Operator 入签名覆盖」2026-09-03 基线）。
// callers = 验签密钥环（AK→SK，当前唯一调用方 zhuzhao）；maxBodyBytes = 读体上限
// （传 cfg.Business.ImportMaxBytes——导入可达 1GiB，勿用 aksk 默认 8MB）。
// 失败按 standards §3.3 信封响应：401+10002 / 413+10001 / 400+10001（通用段）。
// 空密钥环 = 全部请求 401（请求级 fail-closed；启动级在 app.InitializeApp）。
func AKSKAuth(callers map[string][]byte, maxBodyBytes int64) gin.HandlerFunc {
	keys := callers
	if keys == nil {
		keys = map[string][]byte{}
	}
	v := &aksk.Verifier{Keys: keys, MaxBodyBytes: maxBodyBytes}
	onFail := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, aksk.ErrBodyTooLarge):
			response.Fail(c, http.StatusRequestEntityTooLarge, 10001, "请求体超过验签读体上限")
		case errors.Is(err, aksk.ErrBodyRead):
			response.BadRequest(c, "请求体读取失败")
		default:
			response.Unauthorized(c, err.Error())
		}
	}
	return aksk.GinMiddleware(v, onFail)
}

// Operator 签名覆盖透传的 X-Operator → ctx（须挂于 AKSKAuth 之后，未经签名
// 校验的请求到不了这里）；缺失回退 "system"（对齐 §9 访问日志兜底口径）。
func Operator() gin.HandlerFunc {
	return func(c *gin.Context) {
		op := c.GetHeader("X-Operator")
		if op == "" {
			op = "system"
		}
		c.Set("operator", op)
		c.Next()
	}
}

// AccessLog 统一访问日志出口（16 号 §9：每请求一行 method/path/status/
// operator/request_id + 参数 4KB 截断；X-Request-ID 由 RequestID 回显响应头）。
// 挂载于全局（探针也留痕）；body 读取后还原，不影响后续中间件与 handler。
func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}
		start := time.Now()
		params := snapshotParams(c)
		c.Next()
		op := c.GetString("operator")
		if op == "" {
			op = "system" // §9：X-Operator 缺失兜底口径
		}
		logger.Info("access",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"operator", op,
			"request_id", c.GetString("request_id"),
			"query", c.Request.URL.RawQuery,
			"params", params,
			"duration_ms", time.Since(start).Milliseconds(),
		)

		// 错误级出口（A6）：消费 c.Errors——当前唯一来源是 export 流中途截断
		//（响应头已发无法改状态码，handler 只能 attach），此前无人消费 = 服务端
		// 无日志的静默失败。带请求上下文单列一行；无错误不产生额外日志。
		if ge := c.Errors.Last(); ge != nil {
			logger.Error("request error",
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"status", c.Writer.Status(),
				"operator", op,
				"request_id", c.GetString("request_id"),
				"err", ge.Err.Error(),
			)
		}
	}
}

// snapshotParams 请求参数快照：query + body 前 4KB。body **只读前 4KB**（LimitReader
// ——禁止 io.ReadAll 全量缓冲：导入 body 可达 1GiB，全量读入 = 每请求同量级内存
// 驻留，且令 handler 层 MaxBytesReader 失去意义），读后以 MultiReader 拼回原
// body——后续中间件（AKSK 全量验签）与 handler 仍见完整流。读失败置空——后续
// 绑定自然报 400，不在此处造第二份错误响应。
func snapshotParams(c *gin.Context) string {
	query := c.Request.URL.RawQuery
	if c.Request.Body == nil {
		return query
	}
	orig := c.Request.Body
	head, err := io.ReadAll(io.LimitReader(orig, 4096))
	if err != nil {
		c.Request.Body = io.NopCloser(bytes.NewBuffer(nil))
		return query
	}
	c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), orig))
	if len(head) == 0 {
		return query
	}
	return query + " " + string(head)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
