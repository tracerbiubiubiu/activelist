package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

func TestValidateTypeName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		wantCh  string // 期望 error_code（有错时）
	}{
		{"合法", "asset_inventory", false, ""},
		{"单字符合法", "a", false, ""},
		{"数字结尾合法", "log2", false, ""},
		{"大写拒绝", "Asset", true, apperr.CodeValidation},
		{"数字开头拒绝", "2fast", true, apperr.CodeValidation},
		{"连字符拒绝", "bad-name", true, apperr.CodeValidation},
		{"空拒绝", "", true, apperr.CodeValidation},
		{"超 51 拒绝（索引名 63 上限）", strings.Repeat("a", 52), true, apperr.CodeValidation},
		{"51 字符合法边界", strings.Repeat("a", 51), false, ""},
		{"系统表 data_types 拒绝", "data_types", true, apperr.CodeReservedField},
		{"系统表 schema_migrations 拒绝", "schema_migrations", true, apperr.CodeReservedField},
		{"pg_ 前缀拒绝", "pg_shadow", true, apperr.CodeValidation},
		{"保留字段名拒绝", "data", true, apperr.CodeReservedField},
		{"保留 status 拒绝", "status", true, apperr.CodeReservedField},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateTypeName(c.in)
			if !c.wantErr {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			require.Equal(t, 422, err.HTTP)
			require.Equal(t, c.wantCh, err.Code)
		})
	}
}

func TestValidateFields(t *testing.T) {
	valid := []meta.Field{
		{Name: "name", Type: "string", Required: true},
		{Name: "qty", Type: "int"},
		{Name: "tags", Type: "string_list"},
		{Name: "scores", Type: "int_list", Sensitive: true},
	}
	require.Nil(t, ValidateFields(valid))
	// 63 = PG 标识符上限（fieldNameRe 首字符 + {0,62}）。
	require.Nil(t, ValidateFields([]meta.Field{{Name: strings.Repeat("a", 63), Type: "string"}}))

	cases := []struct {
		name   string
		fields []meta.Field
		want   string
	}{
		{"空字段表拒绝", nil, apperr.CodeValidation},
		{"字段名超 63 拒绝", []meta.Field{{Name: strings.Repeat("a", 64), Type: "string"}}, apperr.CodeValidation},
		{"类型非法", []meta.Field{{Name: "x", Type: "boolean"}}, apperr.CodeValidation},
		{"类型大小写敏感", []meta.Field{{Name: "x", Type: "String"}}, apperr.CodeValidation},
		{"字段名大写拒绝", []meta.Field{{Name: "Name", Type: "string"}}, apperr.CodeValidation},
		{"保留 id 拒绝", []meta.Field{{Name: "id", Type: "int"}}, apperr.CodeReservedField},
		{"保留 created_at 拒绝", []meta.Field{{Name: "created_at", Type: "string"}}, apperr.CodeReservedField},
		{"重复名拒绝", []meta.Field{{Name: "a", Type: "int"}, {Name: "a", Type: "string"}}, apperr.CodeValidation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateFields(c.fields)
			require.NotNil(t, err)
			require.Equal(t, 422, err.HTTP)
			require.Equal(t, c.want, err.Code)
		})
	}
}

// GetRules 与强制校验同源：前端按展示规则放行的输入，提交时不得被 422 拒绝
// （防「展示的规则 ≠ 强制的规则」漂移——/rules 端点即为此契约存在）。
func TestGetRules_ConsistentWithEnforcement(t *testing.T) {
	r := GetRules()

	// 正则串与编译正则同串（同源即同义）
	require.Equal(t, typeNameRe.String(), r.TypeName.Pattern)
	require.Equal(t, fieldNameRe.String(), r.Fields.NamePattern)

	// 广告的长度上限与强制正则边界一致：上限内必过、超 1 位必拒
	require.Nil(t, ValidateTypeName(strings.Repeat("a", r.TypeName.MaxLength)))
	require.NotNil(t, ValidateTypeName(strings.Repeat("a", r.TypeName.MaxLength+1)))
	require.Nil(t, ValidateFields([]meta.Field{
		{Name: strings.Repeat("a", r.Fields.NameMaxLength), Type: "string"}}))
	require.NotNil(t, ValidateFields([]meta.Field{
		{Name: strings.Repeat("a", r.Fields.NameMaxLength+1), Type: "string"}}))

	// 清单与强制 map 同源（升序稳定输出）
	require.Equal(t, sortedKeys(reservedTables), r.TypeName.ReservedTables)
	require.Equal(t, sortedKeys(reservedFields), r.TypeName.ReservedFields)
	require.Equal(t, sortedKeys(reservedFields), r.Fields.ReservedFields)
	require.Equal(t, sortedKeys(fieldTypes), r.Fields.AllowedTypes)

	// 禁用前缀一致： advertised 前缀开头必拒
	require.NotNil(t, ValidateTypeName(r.TypeName.ForbiddenPrefix[0]+"x"))

	// 允许类型逐个可过、典型表外类型必拒
	for _, typ := range r.Fields.AllowedTypes {
		require.Nil(t, ValidateFields([]meta.Field{{Name: "x", Type: typ}}))
	}
	require.NotNil(t, ValidateFields([]meta.Field{{Name: "x", Type: "boolean"}}))
}
