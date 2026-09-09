// 数据端点绑定/游标解析负向单测（nil service 即可跑——断言路径全部在触达
// service 前返回；业务正路径由集成测试盖）。
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDataBindingNegatives(t *testing.T) {
	r := New(Deps{}) // Types/Data 均为 nil
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"游标缺 after_id", httptest.NewRequest(http.MethodGet,
			"/api/v1/data/k?after_created_at=2026-09-08T00:00:00Z", nil)},
		{"游标缺 after_created_at", httptest.NewRequest(http.MethodGet,
			"/api/v1/data/k?after_id=5", nil)},
		{"游标时间非 RFC3339", httptest.NewRequest(http.MethodGet,
			"/api/v1/data/k?after_created_at=yesterday&after_id=5", nil)},
		{"游标 id 非整数", httptest.NewRequest(http.MethodGet,
			"/api/v1/data/k?after_created_at=2026-09-08T00:00:00Z&after_id=x", nil)},
		{"page_size 非整数", httptest.NewRequest(http.MethodGet,
			"/api/v1/data/k?page_size=abc", nil)},
		{"路径 id 非整数", httptest.NewRequest(http.MethodGet, "/api/v1/data/k/abc", nil)},
		{"插入 body 非法 JSON", httptest.NewRequest(http.MethodPost, "/api/v1/data/k",
			strings.NewReader(`{bad`))},
		{"插入缺 data", httptest.NewRequest(http.MethodPost, "/api/v1/data/k",
			strings.NewReader(`{}`))},
		{"插入 data 非对象", httptest.NewRequest(http.MethodPost, "/api/v1/data/k",
			strings.NewReader(`{"data":[1,2]}`))},
		{"更新缺 version", httptest.NewRequest(http.MethodPost, "/api/v1/data/k/1/update",
			strings.NewReader(`{"data":{"a":1}}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, tc.req)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"code":10001`)
		})
	}
}
