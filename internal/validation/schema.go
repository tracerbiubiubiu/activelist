// Package validation schema/名称白名单校验（M-A2）。
// 规则源 = implementation-plan §1（最终画像收窄：type ∈ int/string/二者列表）
// + §7（保留字段、sensitive 标记预留）。
package validation

import (
	"regexp"
	"strings"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

// typeNameRe/fieldNameRe 标识符白名单：小写字母开头，仅小写字母/数字/下划线。
// typeName 同时是动态表名（≤63 = PG 标识符上限），白名单本身即注入防线，
// DDL 中仍统一双引号引用。
var (
	// typeName ≤51：动态索引名 idx_<name>_created 须 ≤63（4+51+8），
	// 超长被 PG 截断后长前缀重名类型会静默共享索引名（IF NOT EXISTS 跳过建索引）。
	typeNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,50}$`)
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// reservedTables 系统表名禁用（typeName 即动态表名）：元数据两表 + 迁移记账表
// 若被注册，CREATE TABLE IF NOT EXISTS 对已存在表静默跳过，后续数据读写
// 将直打元数据表——完整性防线必须在白名单层拦截。
var reservedTables = map[string]bool{
	"data_types": true, "data_type_schema_history": true, "schema_migrations": true,
}

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

// Rules 类型与字段创建规则（前端创建类型弹窗展示用；规则变更前端自动同步，
// 无需硬编码——GET /api/v1/admin/types/rules 返回此结构）。
type Rules struct {
	TypeName NameRule   `json:"type_name"`
	Fields   FieldRules `json:"fields"`
}

// NameRule 命名规则（类型名/字段名共用结构）。
type NameRule struct {
	Pattern         string   `json:"pattern"`          // 正则
	PatternDesc     string   `json:"pattern_desc"`     // 人读描述
	MaxLength       int      `json:"max_length"`       // 含首字符总长上限
	ReservedFields  []string `json:"reserved_fields"`  // 保留字段禁用清单
	ReservedTables  []string `json:"reserved_tables"`  // 系统表名禁用清单（仅类型名）
	ForbiddenPrefix []string `json:"forbidden_prefix"` // 禁用前缀（仅类型名）
}

// FieldRules 字段定义集合规则。
type FieldRules struct {
	NamePattern     string   `json:"name_pattern"`      // 字段名正则
	NamePatternDesc string   `json:"name_pattern_desc"` // 人读描述
	NameMaxLength   int      `json:"name_max_length"`   // 含首字符总长上限
	ReservedFields  []string `json:"reserved_fields"`   // 保留字段禁用清单
	AllowedTypes    []string `json:"allowed_types"`     // 允许的字段类型
	MinFields       int      `json:"min_fields"`        // 最少字段数
	NoDuplicate     bool     `json:"no_duplicate"`      // 禁止重复字段名
}

// reservedFieldsList 保留字段清单（错误消息展示用，人读友好序）。
const reservedFieldsList = "id, version, status, created_at, updated_at, created_by, updated_by, data"

func invalid(msg string) *apperr.Error {
	return apperr.New(422, apperr.CodeValidation, msg)
}

// GetRules 返回类型与字段创建规则（前端弹窗展示用）。
func GetRules() Rules {
	return Rules{
		TypeName: NameRule{
			Pattern:         `^[a-z][a-z0-9_]{0,50}$`,
			PatternDesc:     "小写字母开头，仅小写字母/数字/下划线",
			MaxLength:       51,
			ReservedFields:  []string{"id", "version", "status", "created_at", "updated_at", "created_by", "updated_by", "data"},
			ReservedTables:  []string{"data_types", "data_type_schema_history", "schema_migrations"},
			ForbiddenPrefix: []string{"pg_"},
		},
		Fields: FieldRules{
			NamePattern:     `^[a-z][a-z0-9_]{0,62}$`,
			NamePatternDesc: "小写字母开头，仅小写字母/数字/下划线",
			NameMaxLength:   63,
			ReservedFields:  []string{"id", "version", "status", "created_at", "updated_at", "created_by", "updated_by", "data"},
			AllowedTypes:    []string{"int", "string", "int_list", "string_list"},
			MinFields:       1,
			NoDuplicate:     true,
		},
	}
}

// ValidateTypeName 类型名白名单。
func ValidateTypeName(name string) *apperr.Error {
	if !typeNameRe.MatchString(name) {
		return invalid("类型名非法（小写字母开头，仅小写字母/数字/下划线，≤51 字符）").
			WithDetail("field", "type_name").WithDetail("value", name)
	}
	if reservedFields[name] {
		return apperr.New(422, apperr.CodeReservedField,
			"类型名与保留字段冲突: "+name+"（保留字段: "+reservedFieldsList+"）").
			WithDetail("field", "type_name").WithDetail("value", name)
	}
	if reservedTables[name] {
		return apperr.New(422, apperr.CodeReservedField, "类型名与系统表冲突: "+name).
			WithDetail("field", "type_name").WithDetail("value", name)
	}
	if strings.HasPrefix(name, "pg_") {
		return invalid("类型名不允许 pg_ 前缀（PG 系统命名空间）").
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
			return apperr.New(422, apperr.CodeReservedField,
				"字段名与保留字段冲突: "+f.Name+"（保留字段: "+reservedFieldsList+"）").
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
