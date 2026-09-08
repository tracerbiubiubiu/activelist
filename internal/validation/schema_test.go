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
		{"超 63 拒绝", strings.Repeat("a", 64), true, apperr.CodeValidation},
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

	cases := []struct {
		name   string
		fields []meta.Field
		want   string
	}{
		{"空字段表拒绝", nil, apperr.CodeValidation},
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
