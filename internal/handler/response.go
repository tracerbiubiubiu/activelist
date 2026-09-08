// §6.8 统一响应信封：{code(=HTTP状态), msg, data, detail{error_code,...}}。
// 成功 data=业务数据；错误 data=null、detail 至少含 error_code。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

type envelope struct {
	Code   int            `json:"code"`
	Msg    string         `json:"msg"`
	Data   any            `json:"data"`
	Detail map[string]any `json:"detail,omitempty"`
}

func render(c *gin.Context, httpStatus int, msg string, data any, detail map[string]any) {
	c.JSON(httpStatus, envelope{Code: httpStatus, Msg: msg, Data: data, Detail: detail})
}

// OK 200 成功。
func OK(c *gin.Context, data any) { render(c, http.StatusOK, "success", data, nil) }

// Created 201 创建成功。
func Created(c *gin.Context, data any) { render(c, http.StatusCreated, "success", data, nil) }

// Fail 业务错误渲染。
func Fail(c *gin.Context, e *apperr.Error) {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	detail["error_code"] = e.Code
	render(c, e.HTTP, e.Msg, nil, detail)
}

// BadRequest 参数解析失败（400；JSON 非法/缺必填）。
func BadRequest(c *gin.Context, msg string) {
	render(c, http.StatusBadRequest, msg, nil, map[string]any{"error_code": apperr.CodeValidation})
}
