// Package validation schema/名称白名单校验（M-A2）。
// 规则源 = implementation-plan §1（最终画像收窄：type ∈ int/string/二者列表）
// + §7（保留字段、sensitive 标记预留）。
package validation

import (
	"regexp"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

// typeNameRe/fieldNameRe 标识符白名单：小写字母开头，仅小写字母/数字/下划线。
// typeName 同时是动态表名（≤63 = PG 标识符上限），白名单本身即注入防线，
// DDL 中仍统一双引号引用。
var (
	typeNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// reservedFields 数据行保留列族（§7）——用户 schema 字段禁用同名。
var reservedFields = map[string]bool{
	"id": true, "version": true, "status": true,
	"created_at": true, "updated_at": true,
	"created_by": true, "updated_by": true, "data": true,
}

// fieldTypes 允许的字段类型（最终画像：int/string/二者的列表）。
var fieldTypes = map[string]bool{
	"int": true, "string": true, "int_list": true, "string_list": true,
}

func invalid(msg string) *apperr.Error {
	return apperr.New(422, apperr.CodeValidation, msg)
}

// ValidateTypeName 类型名白名单。
func ValidateTypeName(name string) *apperr.Error {
	if !typeNameRe.MatchString(name) {
		return invalid("类型名非法（小写字母开头，仅小写字母/数字/下划线，≤63 字符）").
			WithDetail("field", "type_name").WithDetail("value", name)
	}
	if reservedFields[name] {
		return apperr.New(422, apperr.CodeReservedField, "类型名与保留字段冲突: "+name).
			WithDetail("field", "type_name").WithDetail("value", name)
	}
	return nil
}

// ValidateFields 字段定义全量校验：类型合法、名称白名单、保留字段、重复名、非空。
func ValidateFields(fields []meta.Field) *apperr.Error {
	if len(fields) == 0 {
		return invalid("fields 不能为空（至少一个字段）").WithDetail("field", "fields")
	}
	seen := map[string]bool{}
	for i, f := range fields {
		if !fieldNameRe.MatchString(f.Name) {
			return invalid("字段名非法（小写字母开头，仅小写字母/数字/下划线，≤63 字符）").
				WithDetail("field", "fields").WithDetail("index", i).WithDetail("value", f.Name)
		}
		if reservedFields[f.Name] {
			return apperr.New(422, apperr.CodeReservedField, "字段名与保留字段冲突: "+f.Name).
				WithDetail("field", "fields").WithDetail("value", f.Name)
		}
		if !fieldTypes[f.Type] {
			return invalid("字段类型非法（允许 int / string / int_list / string_list）").
				WithDetail("field", f.Name).WithDetail("value", f.Type)
		}
		if seen[f.Name] {
			return invalid("字段名重复: "+f.Name).
				WithDetail("field", "fields").WithDetail("value", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}
