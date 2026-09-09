// Package apperr activelist 业务错误（§6.8 契约：HTTP 状态码 + 稳定 error_code
// 字符串常量 + 人读消息；handler 层统一渲染为 {code, msg, data, detail} 信封）。
package apperr

import "fmt"

// error_code 字符串常量（§6.8——跨仓契约，勿改值）。
const (
	CodeValidation    = "VALIDATION_ERROR"       // Schema 校验失败 → 422
	CodeReservedField = "RESERVED_FIELD"         // 字段名与保留字段冲突 → 422
	CodeTypeNotFound  = "TYPE_NOT_FOUND"         // 类型不存在 → 404
	CodeTypeExists    = "TYPE_ALREADY_EXISTS"    // 类型已存在 → 409
	CodeTypeDepr      = "TYPE_DEPRECATED"        // 类型已废弃不可写入 → 409
	CodeDataNotFound  = "DATA_NOT_FOUND"         // 数据行不存在 → 404
	CodeFieldDepr     = "FIELD_DEPRECATED"       // 旧数据携带已移除字段 → 422（懒执行迁移提示，§4）
	CodeNewRequired   = "NEW_REQUIRED_FIELD"     // 旧数据缺演进新增必填字段 → 422（懒执行迁移提示，§4）
	CodeConflict      = "CONFLICT"               // 并发冲突/版本不匹配 → 409
	CodeInternal      = "INTERNAL_ERROR"         // 内部错误 → 500
	CodeDependency    = "DEPENDENCY_UNAVAILABLE" // 依赖不可用 → 503
)

// Error 业务错误。HTTP 为响应状态码，Code 为 detail.error_code。
type Error struct {
	HTTP   int
	Code   string
	Msg    string
	Detail map[string]any
}

func (e *Error) Error() string { return fmt.Sprintf("[%s] %s", e.Code, e.Msg) }

// New 构造业务错误。
func New(httpStatus int, code, msg string) *Error {
	return &Error{HTTP: httpStatus, Code: code, Msg: msg}
}

// WithDetail 附加 detail 字段（链式）。
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	e.Detail[k] = v
	return e
}
