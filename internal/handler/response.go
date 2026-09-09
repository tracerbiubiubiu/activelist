// 统一响应信封（standards §3.3）：utils response——
// {code(业务码,0=成功), message, data, request_id}；创建类端点同 200（无 201/204）。
// 业务错误 HTTP 状态表重试语义（4xx 不可重试 / 5xx 可重试）；响应体不带状态字段；
// apperr.Detail（expected_version/type_name 等）渲染时折叠进 message——信封无 detail 字段。
package handler

import (
	"fmt"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/zhuzhao-utils/response"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

// OK 成功（200；创建类端点同 200）。
func OK(c *gin.Context, data any) { response.OK(c, data) }

// BadRequest 参数解析失败（400 + 通用段 10001）。
func BadRequest(c *gin.Context, msg string) { response.BadRequest(c, msg) }

// Fail 业务错误渲染：code = 跨服务数值业务码（apperr.Error.Num）。
func Fail(c *gin.Context, e *apperr.Error) {
	response.Fail(c, e.HTTP, e.Num(), composeMsg(e))
}

// composeMsg 人读消息 + Detail 上下文折叠（键名字典序，输出稳定）。
func composeMsg(e *apperr.Error) string {
	if len(e.Detail) == 0 {
		return e.Msg
	}
	keys := make([]string, 0, len(e.Detail))
	for k := range e.Detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := e.Msg + "（"
	for i, k := range keys {
		if i > 0 {
			s += "；"
		}
		s += fmt.Sprintf("%s=%v", k, e.Detail[k])
	}
	return s + "）"
}
