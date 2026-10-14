// meta 包单测（2026-10-14 契约审计 B3 护栏）：Field Sensitive/Required 恒出 +
// scanDef nil Fields → [] 兜底 + 404 映射——纯内存，无需 PG。
package meta

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// rowFake pgx.Row 桩：按位置回填值（列序 = selectCols）或返回预设错误。
type rowFake struct {
	vals []any
	err  error
}

func (r rowFake) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return errors.New("列数不符")
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.vals[i]))
	}
	return nil
}

func defRow(raw string) rowFake {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	return rowFake{vals: []any{"t1", []byte(raw), "active", int64(1), now, now}}
}

func TestField_SensitiveAlwaysEmitted(t *testing.T) {
	b, err := json.Marshal(Field{Name: "qty", Type: "int"})
	require.NoError(t, err)
	require.Contains(t, string(b), `"sensitive":false`, "Sensitive 无 omitempty：false 不得消失")
	require.Contains(t, string(b), `"required":false`)
}

func TestScanDef_NilFieldsFallback(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"fields":null}`} {
		def, err := scanDef(defRow(raw))
		require.NoError(t, err, raw)
		require.NotNil(t, def.Fields, raw)
		require.Empty(t, def.Fields, raw)
		out, err := json.Marshal(def)
		require.NoError(t, err)
		require.Contains(t, string(out), `"fields":[]`, raw)
	}
}

func TestScanDef_FieldsParsed(t *testing.T) {
	def, err := scanDef(defRow(`{"fields":[{"name":"qty","type":"int","required":true,"sensitive":false}]}`))
	require.NoError(t, err)
	require.Equal(t, []Field{{Name: "qty", Type: "int", Required: true, Sensitive: false}}, def.Fields)
}

func TestScanDef_NotFoundMaps404(t *testing.T) {
	_, err := scanDef(rowFake{err: pgx.ErrNoRows})
	require.Error(t, err)
	require.Contains(t, err.Error(), "类型不存在")
}
