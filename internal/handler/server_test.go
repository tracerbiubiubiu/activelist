package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// 健康探针契约（M-A1 退出标准：健康检查过）。
func TestHealthz(t *testing.T) {
	w := httptest.NewRecorder()
	New(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

func TestReadyz(t *testing.T) {
	t.Run("依赖就绪 200", func(t *testing.T) {
		w := httptest.NewRecorder()
		New(Deps{Ready: func() error { return nil }}).
			ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.JSONEq(t, `{"status":"ready"}`, w.Body.String())
	})
	t.Run("PG 不可查询 503（err 透出排障）", func(t *testing.T) {
		w := httptest.NewRecorder()
		New(Deps{Ready: func() error { return errors.New("ping failed") }}).
			ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Contains(t, w.Body.String(), "unready")
	})
	t.Run("RequestID 回显（缺省自生成）", func(t *testing.T) {
		w := httptest.NewRecorder()
		New(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		require.NotEmpty(t, w.Header().Get("X-Request-ID"))
	})
}
