// 数据行校验（M-A3）：写路径对当前 schema 全量校验，读取零校验（§1.3 方案 D）。
// 更新场景由 service 先「旧行 data ⊕ body」合并后再进本函数，故此处看到的
// 永远是合并后的完整数据——required 判定、未知键判定都基于合并结果。
package validation

import (
	"math"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

// ValidateData 合并后数据全量校验：键 ⊆ schema、required 在场非 null、值类型匹配。
// null 视为「未提供」：required + null = 缺失 422；optional + null 放行（清值语义）。
// 未知键 422：动态模型唯一防线是 schema 校验本身，放行拼写错误的键=数据静默丢失；
// 键撞保留列族分 RESERVED_FIELD 档（与类型注册同口径）。
func ValidateData(fields []meta.Field, data map[string]any) *apperr.Error {
	known := make(map[string]meta.Field, len(fields))
	for _, f := range fields {
		known[f.Name] = f
	}
	for k, v := range data {
		f, ok := known[k]
		if !ok {
			if reservedFields[k] {
				return apperr.New(422, apperr.CodeReservedField, "字段名与保留字段冲突: "+k).
					WithDetail("field", k)
			}
			return invalid("未知字段（schema 中不存在）: "+k).
				WithDetail("field", k).WithDetail("reason", "unknown_field")
		}
		if err := checkValue(f, v); err != nil {
			return err
		}
	}
	for _, f := range fields {
		if v, ok := data[f.Name]; (!ok || v == nil) && f.Required {
			return invalid("缺少必填字段: "+f.Name).
				WithDetail("field", f.Name).WithDetail("reason", "missing_required")
		}
	}
	return nil
}

// checkValue 单值类型检查（字段类型标识符与 schema.go fieldTypes 同源：
// int / string / int_list / string_list）。JSON 数字经 encoding/json 解为
// float64，int 须无小数部分——>2^53 整数精度丢失为已知限制（JSONB 存储与
// JS 客户端同界，方案文档未承诺大整数）。
func checkValue(f meta.Field, v any) *apperr.Error {
	if v == nil {
		return nil // required 缺失由遍历 fields 的第二遍统一报，此处不重复
	}
	badType := func() *apperr.Error {
		return invalid("字段类型不符（期望 "+f.Type+"）").
			WithDetail("field", f.Name).WithDetail("value", v)
	}
	switch f.Type {
	case "int":
		if !isJSONInt(v) {
			return badType()
		}
	case "string":
		if _, ok := v.(string); !ok {
			return badType()
		}
	case "int_list":
		list, ok := v.([]any)
		if !ok {
			return badType()
		}
		for i, el := range list {
			if !isJSONInt(el) {
				return invalid("列表元素类型不符（期望 int）").
					WithDetail("field", f.Name).WithDetail("index", i).WithDetail("value", el)
			}
		}
	case "string_list":
		list, ok := v.([]any)
		if !ok {
			return badType()
		}
		for i, el := range list {
			if _, ok := el.(string); !ok {
				return invalid("列表元素类型不符（期望 string）").
					WithDetail("field", f.Name).WithDetail("index", i).WithDetail("value", el)
			}
		}
	}
	return nil
}

// isJSONInt JSON 整数判定（含列表元素复用）。
func isJSONInt(v any) bool {
	n, ok := v.(float64)
	return ok && n == math.Trunc(n)
}
