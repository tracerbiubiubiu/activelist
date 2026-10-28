// Package validation schema/名称白名单校验（M-A2）。
// 规则源 = implementation-plan §1（最终画像收窄：type ∈ int/string/二者列表）
// + §7（保留字段、sensitive 标记预留）。
package validation

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

// 规则常量（单一事实源）：编译正则、GetRules 前端展示、错误消息同源取值，
// 改规则只改这里——防「展示的规则 ≠ 强制的规则」漂移。
const (
	typeNamePattern  = `^[a-z][a-z0-9_]{0,50}$`
	fieldNamePattern = `^[a-z][a-z0-9_]{0,62}$`
	// typeNameMaxLen ≤51：动态索引名 idx_<name>_created 须 ≤63（4+51+8），
	// 超长被 PG 截断后长前缀重名类型会静默共享索引名（IF NOT EXISTS 跳过建索引）。
	typeNameMaxLen  = 51
	fieldNameMaxLen = 63 // PG 标识符上限
	forbiddenPrefix = "pg_"

	// stringMaxCharsDefault string/string_list 单值字符上限缺省（2026-10-27 所有者
	// 拍板：255 字符起步——拦「整篇文章贴进来」级事故，业务上足够宽松）；
	// Field.MaxChars=0（存量 schema 未配置）回退此值。字符数=Runes 非字节。
	stringMaxCharsDefault = 255
	// stringMaxCharsMax 注册时可配置的 max_chars 上界（资源防护硬顶，防「配置出
	// 一个 GB 级上限」架空护栏——与导入端点 1GiB MaxBytesReader 同哲学）。
	stringMaxCharsMax = 65535

	// listMaxItemsDefault int_list/string_list 项数上限缺省（2026-10-27 同批拍板：
	// 100 项起步——拦「整列粘贴」级事故；正当大清单走字段级 max_items 配置）。
	listMaxItemsDefault = 100
	// listMaxItemsMax 项数可配置上界（同 stringMaxCharsMax 资源硬顶哲学）。
	listMaxItemsMax = 65535
)

// typeNameRe/fieldNameRe 标识符白名单：小写字母开头，仅小写字母/数字/下划线。
// typeName 同时是动态表名，白名单本身即注入防线，DDL 中仍统一双引号引用。
var (
	typeNameRe  = regexp.MustCompile(typeNamePattern)
	fieldNameRe = regexp.MustCompile(fieldNamePattern)
)

// reservedTables 系统表名禁用（typeName 即动态表名）：元数据两表 + 迁移记账表
// 若被注册，CREATE TABLE IF NOT EXISTS 对已存在表静默跳过，后续数据读写
// 将直打元数据表——完整性防线必须在白名单层拦截。
var reservedTables = map[string]bool{
	"data_types": true, "data_type_schema_history": true, "schema_migrations": true,
	// 审计修正（2026-09-30 二轮）：路由静态动词段——注册同名的类型会被 gin 静态
	// 路由遮蔽（restore 遮蔽插入端点/rules 遮蔽定义读取），形成半残类型
	"restore": true, "rules": true, "export": true, "import": true, "history": true,
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

// sortedKeys map 键升序输出（GetRules 的 JSON 契约要求稳定排序）。
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	NamePattern           string   `json:"name_pattern"`             // 字段名正则
	NamePatternDesc       string   `json:"name_pattern_desc"`        // 人读描述
	NameMaxLength         int      `json:"name_max_length"`          // 含首字符总长上限
	ReservedFields        []string `json:"reserved_fields"`          // 保留字段禁用清单
	AllowedTypes          []string `json:"allowed_types"`            // 允许的字段类型
	MinFields             int      `json:"min_fields"`               // 最少字段数
	NoDuplicate           bool     `json:"no_duplicate"`             // 禁止重复字段名
	StringMaxCharsDefault int      `json:"string_max_chars_default"` // string/string_list 单值字符上限缺省
	StringMaxCharsMax     int      `json:"string_max_chars_max"`     // max_chars 可配置上界
	ListMaxItemsDefault   int      `json:"list_max_items_default"`   // int_list/string_list 项数上限缺省
	ListMaxItemsMax       int      `json:"list_max_items_max"`       // max_items 可配置上界
}

// ValidateUniqueFields 唯一键声明校验（2026-10-27 所有者提出——多源汇聚整体
// 去重）：空=无唯一约束放行；每个键须为 schema 中存在的**标量**字段
// （int/string——列表类型作唯一键语义复杂，第一档排除）且不得重复出现。
func ValidateUniqueFields(fields []meta.Field, unique []string) *apperr.Error {
	if len(unique) == 0 {
		return nil
	}
	known := make(map[string]meta.Field, len(fields))
	for _, f := range fields {
		known[f.Name] = f
	}
	seen := map[string]bool{}
	for _, name := range unique {
		f, ok := known[name]
		if !ok {
			return invalid("唯一键字段不存在: "+name).
				WithDetail("field", name)
		}
		if f.Type == "int_list" || f.Type == "string_list" {
			return invalid("列表类型字段不可作唯一键（仅 int/string）").
				WithDetail("field", name).WithDetail("type", f.Type)
		}
		if seen[name] {
			return invalid("唯一键字段重复: "+name).
				WithDetail("field", name)
		}
		seen[name] = true
	}
	return nil
}

// reservedFieldsList 保留字段清单（错误消息展示用，人读友好序）。
const reservedFieldsList = "id, version, status, created_at, updated_at, created_by, updated_by, data"

func invalid(msg string) *apperr.Error {
	return apperr.New(422, apperr.CodeValidation, msg)
}

// GetRules 返回类型与字段创建规则（前端弹窗展示用）。全部字段自上方常量与
// 校验 map 同源派生——展示与强制校验是同一套规则，不存在第二份可漂移的副本。
func GetRules() Rules {
	return Rules{
		TypeName: NameRule{
			Pattern:         typeNamePattern,
			PatternDesc:     "小写字母开头，仅小写字母/数字/下划线",
			MaxLength:       typeNameMaxLen,
			ReservedFields:  sortedKeys(reservedFields),
			ReservedTables:  sortedKeys(reservedTables),
			ForbiddenPrefix: []string{forbiddenPrefix},
		},
		Fields: FieldRules{
			NamePattern:           fieldNamePattern,
			NamePatternDesc:       "小写字母开头，仅小写字母/数字/下划线",
			NameMaxLength:         fieldNameMaxLen,
			ReservedFields:        sortedKeys(reservedFields),
			AllowedTypes:          sortedKeys(fieldTypes),
			MinFields:             1,
			NoDuplicate:           true,
			StringMaxCharsDefault: stringMaxCharsDefault,
			StringMaxCharsMax:     stringMaxCharsMax,
			ListMaxItemsDefault:   listMaxItemsDefault,
			ListMaxItemsMax:       listMaxItemsMax,
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
	if strings.HasPrefix(name, forbiddenPrefix) {
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
		// max_chars 仅 string/string_list 可配（int 家族无长度概念——语义错配在
		// 注册期拒绝，不静默忽略；0=缺省合法）
		if f.MaxChars != 0 {
			if f.Type == "int" || f.Type == "int_list" {
				return invalid("int 类型字段不支持 max_chars（长度上限仅作用于 string/string_list）").
					WithDetail("field", f.Name).WithDetail("max_chars", f.MaxChars)
			}
			if f.MaxChars < 0 || f.MaxChars > stringMaxCharsMax {
				return invalid("max_chars 超界（1–"+strconv.Itoa(stringMaxCharsMax)+"，0=缺省 "+strconv.Itoa(stringMaxCharsDefault)+"）").
					WithDetail("field", f.Name).WithDetail("max_chars", f.MaxChars)
			}
		}
		// max_items 仅 int_list/string_list 可配（同上语义错配口径）
		if f.MaxListItems != 0 {
			if f.Type == "int" || f.Type == "string" {
				return invalid("非列表类型字段不支持 max_list_items（项数上限仅作用于 int_list/string_list）").
					WithDetail("field", f.Name).WithDetail("max_list_items", f.MaxListItems)
			}
			if f.MaxListItems < 0 || f.MaxListItems > listMaxItemsMax {
				return invalid("max_list_items 超界（1–"+strconv.Itoa(listMaxItemsMax)+"，0=缺省 "+strconv.Itoa(listMaxItemsDefault)+"）").
					WithDetail("field", f.Name).WithDetail("max_list_items", f.MaxListItems)
			}
		}
		if seen[f.Name] {
			return invalid("字段名重复: "+f.Name).
				WithDetail("field", "fields").WithDetail("value", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}
