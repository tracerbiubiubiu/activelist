// transfer 流式编解码单元测试（无 DB）：往返一致 / 分批边界 / 非法输入 / handle 中断。
package transfer

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tracerbiubiubiu/activelist/internal/repository"
)

var errTest = errors.New("handle error")

func sampleDoc(id int64, name string) repository.Document {
	return repository.Document{
		ID: id, Version: 1, Status: "active",
		Data: map[string]any{"name": name, "qty": float64(id)},
	}
}

func encodeN(t *testing.T, n int) []byte {
	t.Helper()
	i := 0
	var buf bytes.Buffer
	require.NoError(t, EncodeDocs(&buf, func() (*repository.Document, error) {
		if i >= n {
			return nil, io.EOF
		}
		i++
		d := sampleDoc(int64(i), "n")
		return &d, nil
	}))
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	raw := encodeN(t, 3)
	var got []repository.Document
	require.NoError(t, DecodeBatches(bytes.NewReader(raw), 2, func(batch []repository.Document) error {
		got = append(got, batch...)
		return nil
	}))
	require.Len(t, got, 3)
	for k := range got {
		require.EqualValues(t, k+1, got[k].ID)
		require.Equal(t, "n", got[k].Data["name"])
	}
}

// 分批边界：5 行 batch=2 → 2/2/1 三批。
func TestDecodeBatches_Batching(t *testing.T) {
	raw := encodeN(t, 5)
	var sizes []int
	require.NoError(t, DecodeBatches(bytes.NewReader(raw), 2, func(batch []repository.Document) error {
		sizes = append(sizes, len(batch))
		return nil
	}))
	require.Equal(t, []int{2, 2, 1}, sizes)
}

// 空数组：合法，零批回调。
func TestRoundTrip_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, EncodeDocs(&buf, func() (*repository.Document, error) { return nil, io.EOF }))
	require.Equal(t, "[]", buf.String())
	require.NoError(t, DecodeBatches(bytes.NewReader(buf.Bytes()), 2, func([]repository.Document) error {
		t.Fatal("空数组不应触发回调")
		return nil
	}))
}

// 非法输入：非数组 / 截断 JSON。
func TestDecodeBatches_Invalid(t *testing.T) {
	err := DecodeBatches(strings.NewReader(`{"not":"array"}`), 2, func([]repository.Document) error { return nil })
	require.ErrorContains(t, err, "JSON 数组")
	err = DecodeBatches(strings.NewReader(`[{"id":1`), 2, func([]repository.Document) error { return nil })
	require.Error(t, err)
}

// handle 错误中断解码（导入事务回滚路径）。
func TestDecodeBatches_HandleErrorAborts(t *testing.T) {
	raw := encodeN(t, 3)
	err := DecodeBatches(bytes.NewReader(raw), 1, func([]repository.Document) error { return errTest })
	require.ErrorIs(t, err, errTest)
}
