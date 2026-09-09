// M-A6 中间件单测：AK/SK 验签（通过/拒绝/上限/空环 fail-closed）+ Operator 透传
// + AccessLog 出口。utils aksk 的窗口/重放语义由其自带测试盖，此处只验接线。
package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"
)

const (
	testAK = "zhuzhao"
	testSK = "sk-it"
)

// newAuthEngine 验签链测试台：捕获 /ping 命中时的 operator 与 request_id。
func newAuthEngine(callers map[string][]byte, maxBody int64) (*gin.Engine, *string, *string) {
	gin.SetMode(gin.ReleaseMode)
	op, rid := "", ""
	r := gin.New()
	r.Use(RequestID(), AKSKAuth(callers, maxBody), Operator())
	r.GET("/ping", func(c *gin.Context) {
		op = c.GetString("operator")
		rid = c.GetString("request_id")
		c.String(http.StatusOK, "pong")
	})
	r.POST("/echo", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r, &op, &rid
}

func TestAKSKAuth_SignedRequestPasses(t *testing.T) {
	r, op, rid := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 1<<20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-ID", "req-fixed")
	aksk.Sign(req, nil, aksk.SignOptions{AK: testAK, SK: []byte(testSK),
		RequestID: "req-fixed", Operator: "op-1"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "op-1", *op)
	require.Equal(t, "req-fixed", *rid)
}

func TestAKSKAuth_MissingOperatorFallsBackToSystem(t *testing.T) {
	r, op, _ := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 1<<20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	aksk.Sign(req, nil, aksk.SignOptions{AK: testAK, SK: []byte(testSK)})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "system", *op)
}

func TestAKSKAuth_WrongSKRejected(t *testing.T) {
	r, _, _ := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 1<<20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	aksk.Sign(req, nil, aksk.SignOptions{AK: testAK, SK: []byte("sk-other")})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), `"code":10002`)
}

func TestAKSKAuth_MissingAuthorizationRejected(t *testing.T) {
	r, _, _ := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 1<<20)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), `"code":10002`)
}

func TestAKSKAuth_EmptyKeyRingFailsClosed(t *testing.T) {
	r, _, _ := newAuthEngine(map[string][]byte{}, 1<<20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	aksk.Sign(req, nil, aksk.SignOptions{AK: testAK, SK: []byte(testSK)})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAKSKAuth_BodyOverLimitRejected413(t *testing.T) {
	r, _, _ := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 8) // 上限 8B
	body := strings.Repeat("x", 100)
	req := httptest.NewRequest(http.MethodPost, "/ping", strings.NewReader(body))
	aksk.Sign(req, []byte(body), aksk.SignOptions{AK: testAK, SK: []byte(testSK)})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), `"code":10001`)
}

func TestAKSKAuth_SignedBodyRoundTripsToHandler(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	got := ""
	r := gin.New()
	r.Use(RequestID(), AKSKAuth(map[string][]byte{testAK: []byte(testSK)}, 1<<20), Operator())
	r.POST("/echo", func(c *gin.Context) {
		b := make([]byte, 64)
		n, _ := c.Request.Body.Read(b)
		got = string(b[:n])
		c.String(http.StatusOK, "ok")
	})
	payload := `{"data":{"name":"n1"}}`
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(payload))
	aksk.Sign(req, []byte(payload), aksk.SignOptions{AK: testAK, SK: []byte(testSK)})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, payload, got, "验签读体后须还原 body，handler 才能完整绑定")
}

func TestAccessLog_EmitsOneLinePerRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(RequestID(), AccessLog(logger))
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping?x=1", nil))

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	require.Equal(t, "access", rec["msg"])
	require.EqualValues(t, http.StatusOK, rec["status"])
	require.Equal(t, "/ping", rec["path"])
	require.Equal(t, "system", rec["operator"]) // 未挂 Operator 中间件时的零值口径
}

func TestAccessLog_NilLoggerNoop(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(AccessLog(nil))
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

// A6 错误级出口：attach 到 c.Errors 的错误由 AccessLog 消费为 ERROR 行
// （request_id/operator 上下文齐全）——export 流中途截断不再服务端无日志。
func TestAccessLog_ErrorLineForAttachedError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(RequestID(), AccessLog(logger))
	r.GET("/boom", func(c *gin.Context) {
		_ = c.Error(errors.New("stream truncated")) // 头已发场景：状态仍是 2xx
		c.String(http.StatusOK, "partial")
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2, "access 行 + request error 行")
	var errLine map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &errLine))
	require.Equal(t, "request error", errLine["msg"])
	require.Equal(t, "stream truncated", errLine["err"])
	require.Equal(t, "ERROR", errLine["level"])
	require.NotEmpty(t, errLine["request_id"])
	require.Equal(t, "system", errLine["operator"])
}

// 时间窗语义抽验：签发时刻拨回 10 分钟（> 默认 ±5min 窗口）→ 401。
func TestAKSKAuth_ExpiredTimestampRejected(t *testing.T) {
	r, _, _ := newAuthEngine(map[string][]byte{testAK: []byte(testSK)}, 1<<20)
	old := time.Now().Add(-10 * time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	aksk.Sign(req, nil, aksk.SignOptions{AK: testAK, SK: []byte(testSK), Now: func() time.Time { return old }})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
