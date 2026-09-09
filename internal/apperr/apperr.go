// Package apperr activelist 业务错误：error_code 字符串常量为内部标识与服务层
// 构造用；线上契约 = standards §3.3 信封 {code(数值业务码), message, data, request_id}
// ——code 取自跨服务段 100000–100999（errcode.md §2/§4），通用语义复用 utils 10000 段；
// HTTP 状态码只表重试语义（4xx 不可重试 / 5xx 可重试）。
package apperr

import "fmt"

// error_code 字符串常量（内部标识；线上以 Num() 数值码为准）。
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

// 数值业务码（跨服务段 100000–100999，2026-09-08 三仓信封收敛）。
const (
	numValidation  = 100000
	numReservedFld = 100001
	numTypeNotFnd  = 100002
	numTypeExists  = 100003
	numTypeDepr    = 100004
	numDataNotFnd  = 100005
	numFieldDepr   = 100006
	numNewRequired = 100007
	numConflict    = 100008
)

// numByCode error_code → 数值业务码；未登记码由 Num() 回退 10000（fail-safe 非 0）。
var numByCode = map[string]int{
	CodeValidation:    numValidation,
	CodeReservedField: numReservedFld,
	CodeTypeNotFound:  numTypeNotFnd,
	CodeTypeExists:    numTypeExists,
	CodeTypeDepr:      numTypeDepr,
	CodeDataNotFound:  numDataNotFnd,
	CodeFieldDepr:     numFieldDepr,
	CodeNewRequired:   numNewRequired,
	CodeConflict:      numConflict,
	CodeInternal:      10000, // utils ErrInternal
	CodeDependency:    10008, // utils ErrServiceUnavailable
}

// Error 业务错误。HTTP 为响应状态码，Code 为内部 error_code 标识，
// Detail 为渲染前折叠进 message 的结构化上下文（信封无 detail 字段，§3.3）。
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

// Num 跨服务数值业务码（信封 code 字段值）。
func (e *Error) Num() int {
	if n, ok := numByCode[e.Code]; ok {
		return n
	}
	return 10000
}

// WithDetail 附加结构化上下文（链式；渲染时折叠进 message）。
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	e.Detail[k] = v
	return e
}
