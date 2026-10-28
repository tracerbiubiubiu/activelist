// ValidateData 单元测试（M-A3）：类型分档 / required / 未知键 / 保留键 / null 语义。
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
)

func sampleFields() []meta.Field {
	return []meta.Field{
		{Name: "name", Type: "string", Required: true},
		{Name: "qty", Type: "int"},
		{Name: "tags", Type: "string_list"},
		{Name: "nums", Type: "int_list"},
	}
}

func TestValidateData(t *testing.T) {
	t.Run("全字段合法", func(t *testing.T) {
		require.Nil(t, ValidateData(sampleFields(), map[string]any{
			"name": "a", "qty": float64(3), "tags": []any{"x", "y"}, "nums": []any{float64(1)},
		}))
	})
	t.Run("部分字段合法（optional 缺省放行）", func(t *testing.T) {
		require.Nil(t, ValidateData(sampleFields(), map[string]any{"name": "a"}))
	})
	t.Run("required 缺失 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{})
		require.Equal(t, 422, err.HTTP)
		require.Equal(t, "VALIDATION_ERROR", err.Code)
		require.Equal(t, "name", err.Detail["field"])
	})
	t.Run("required 显式 null 等价缺失 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": nil})
		require.Equal(t, 422, err.HTTP)
	})
	t.Run("optional null 放行（清值语义）", func(t *testing.T) {
		require.Nil(t, ValidateData(sampleFields(), map[string]any{"name": "a", "qty": nil}))
	})
	t.Run("int 收到字符串 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "qty": "3"})
		require.Equal(t, 422, err.HTTP)
		require.Equal(t, "qty", err.Detail["field"])
	})
	t.Run("int 小数部分 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "qty": 1.5})
		require.Equal(t, 422, err.HTTP)
	})
	t.Run("int 整数值 float 放行（JSON 数字解为 float64）", func(t *testing.T) {
		require.Nil(t, ValidateData(sampleFields(), map[string]any{"name": "a", "qty": float64(7)}))
	})
	t.Run("string 收到数字 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": 1})
		require.Equal(t, 422, err.HTTP)
	})
	t.Run("string_list 元素类型 422（含 index 定位）", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "tags": []any{"x", 2}})
		require.Equal(t, 422, err.HTTP)
		require.Equal(t, "tags", err.Detail["field"])
		require.Equal(t, 1, err.Detail["index"])
	})
	t.Run("int_list 非数组 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "nums": "no"})
		require.Equal(t, 422, err.HTTP)
	})
	t.Run("空列表放行", func(t *testing.T) {
		require.Nil(t, ValidateData(sampleFields(), map[string]any{"name": "a", "tags": []any{}}))
	})
	t.Run("未知字段 422", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "typo_field": 1})
		require.Equal(t, 422, err.HTTP)
		require.Equal(t, "VALIDATION_ERROR", err.Code)
		require.Equal(t, "typo_field", err.Detail["field"])
	})
	t.Run("保留列族键分 RESERVED_FIELD 档", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{"name": "a", "id": 1})
		require.Equal(t, 422, err.HTTP)
		require.Equal(t, "RESERVED_FIELD", err.Code)
	})
	t.Run("reason 分档供懒执行映射（M-A4）", func(t *testing.T) {
		err := ValidateData(sampleFields(), map[string]any{})
		require.Equal(t, "missing_required", err.Detail["reason"])
		err = ValidateData(sampleFields(), map[string]any{"name": "a", "oops": 1})
		require.Equal(t, "unknown_field", err.Detail["reason"])
	})
	t.Run("nil data 等价空对象（required 兜底报错）", func(t *testing.T) {
		err := ValidateData(sampleFields(), nil)
		require.Equal(t, 422, err.HTTP)
	})
}

// 防呆：四类字段类型对错误值都必须报错——未来新增类型标识符忘实现校验即在此暴露。
func TestValidateData_CoversAllFieldTypes(t *testing.T) {
	for _, ft := range []string{"int", "string", "int_list", "string_list"} {
		fields := []meta.Field{{Name: "f", Type: ft}}
		err := ValidateData(fields, map[string]any{"f": "definitely-wrong"})
		require.Error(t, err, "类型 %s 对错误值应报错", ft)
		var ae *apperr.Error
		require.ErrorAs(t, err, &ae)
	}
}

// string/string_list 字符上限（2026-10-27 拍板）：rune 计数非字节、0=缺省 255、
// 自定义 max_len 生效、列表元素同界。
func TestValidateData_StringMaxLen(t *testing.T) {
	t.Run("缺省 255：恰 255 字符过、256 拒", func(t *testing.T) {
		fields := []meta.Field{{Name: "s", Type: "string"}}
		require.Nil(t, ValidateData(fields, map[string]any{"s": strings.Repeat("a", 255)}))
		err := ValidateData(fields, map[string]any{"s": strings.Repeat("a", 256)})
		require.NotNil(t, err)
		require.Equal(t, 422, err.HTTP)
		require.EqualValues(t, 256, err.Detail["length"])
		require.EqualValues(t, 255, err.Detail["max_len"])
	})
	t.Run("字符数非字节数：255 个中文（765 字节）放行、256 个拒", func(t *testing.T) {
		fields := []meta.Field{{Name: "s", Type: "string"}}
		require.Nil(t, ValidateData(fields, map[string]any{"s": strings.Repeat("中", 255)}))
		require.NotNil(t, ValidateData(fields, map[string]any{"s": strings.Repeat("中", 256)}))
	})
	t.Run("自定义 max_len 生效", func(t *testing.T) {
		fields := []meta.Field{{Name: "s", Type: "string", MaxLen: 5}}
		require.Nil(t, ValidateData(fields, map[string]any{"s": "abcde"}))
		require.NotNil(t, ValidateData(fields, map[string]any{"s": "abcdef"}))
	})
	t.Run("string_list 元素同界（带 index）", func(t *testing.T) {
		fields := []meta.Field{{Name: "tags", Type: "string_list", MaxLen: 3}}
		require.Nil(t, ValidateData(fields, map[string]any{"tags": []any{"abc", "de"}}))
		err := ValidateData(fields, map[string]any{"tags": []any{"ok", "too-long"}})
		require.NotNil(t, err)
		require.EqualValues(t, 1, err.Detail["index"])
	})
	t.Run("optional 字段 null 放行（长度校验不误伤清值语义）", func(t *testing.T) {
		fields := []meta.Field{{Name: "s", Type: "string", MaxLen: 3}}
		require.Nil(t, ValidateData(fields, map[string]any{"s": nil}))
	})
}

// int_list/string_list 项数上限（2026-10-27 同批拍板）：0=缺省 100、自定义生效、
// 双列表类型同界。
func TestValidateData_ListMaxItems(t *testing.T) {
	mk := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = float64(i)
		}
		return out
	}
	t.Run("缺省 100：恰 100 项过、101 拒（int_list）", func(t *testing.T) {
		fields := []meta.Field{{Name: "nums", Type: "int_list"}}
		require.Nil(t, ValidateData(fields, map[string]any{"nums": mk(100)}))
		err := ValidateData(fields, map[string]any{"nums": mk(101)})
		require.NotNil(t, err)
		require.Equal(t, 422, err.HTTP)
		require.EqualValues(t, 101, err.Detail["count"])
		require.EqualValues(t, 100, err.Detail["max_items"])
	})
	t.Run("自定义 max_items 生效（string_list）", func(t *testing.T) {
		fields := []meta.Field{{Name: "tags", Type: "string_list", MaxItems: 2}}
		require.Nil(t, ValidateData(fields, map[string]any{"tags": []any{"a", "b"}}))
		err := ValidateData(fields, map[string]any{"tags": []any{"a", "b", "c"}})
		require.NotNil(t, err)
		require.EqualValues(t, 3, err.Detail["count"])
	})
	t.Run("项数校验先于元素校验不越界误报（超限即报项数）", func(t *testing.T) {
		fields := []meta.Field{{Name: "nums", Type: "int_list", MaxItems: 2}}
		err := ValidateData(fields, map[string]any{"nums": []any{float64(1), "not-int", float64(3)}})
		require.NotNil(t, err)
		require.Contains(t, err.Msg, "项数超限")
	})
}
