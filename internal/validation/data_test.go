// ValidateData 单元测试（M-A3）：类型分档 / required / 未知键 / 保留键 / null 语义。
package validation

import (
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
