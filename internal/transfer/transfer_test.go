// transfer 流式编解码单元测试（无 DB）：往返一致 / 分批边界 / 非法输入 / handle 中断。
package transfer

import (
	"bytes"
	"errors"
	"io"
	"net/http"
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

// 文件尾核验（CMP-R02）：首个数组闭合后必须只剩尾随空白。第二段 payload 一旦
// 被静默忽略，全量替换导入会拿“部分文件”当全部提交成功。前缀合法行仍整批回调
// （导入事务随后回滚），但整体必须拒绝并归 400（badFile，非 413）。
func TestDecodeBatches_TrailingContentRejected(t *testing.T) {
	valid := `[{"id":"1","version":1,"data":{"name":"n"}}]` // id,string 公约（repository.Document）
	cases := []struct {
		name    string
		raw     string
		handled int
	}{
		{"second-array", valid + `[{"id":"2","version":1,"data":{"name":"x"}}]`, 1},
		{"two-arrays", valid + valid, 1},
		{"two-empty-arrays", `[] []`, 0},
		{"trailing-closed", valid + `]`, 1},
		{"trailing-object", valid + `{"id":"2"}`, 1},
		{"trailing-garbage", valid + `garbage`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var handled int
			err := DecodeBatches(strings.NewReader(tc.raw), 2, func(batch []repository.Document) error {
				handled += len(batch)
				return nil
			})
			require.ErrorContains(t, err, "JSON 数组")
			require.Equal(t, tc.handled, handled, "首个数组仍应整批回调后整体拒绝")
		})
	}
}

// 合法尾随空白不拒绝：导出侧 EncodeDocs 以 ']' 结束，导入侧容许调用方追加换行等空白。
func TestDecodeBatches_TrailingWhitespaceAccepted(t *testing.T) {
	var handled int
	require.NoError(t, DecodeBatches(strings.NewReader(`[{"id":"1","version":1}]`+"\n\t \r\n"), 2, func(batch []repository.Document) error {
		handled += len(batch)
		return nil
	}))
	require.Equal(t, 1, handled)
}

// 尾随内容触顶仍透传 *http.MaxBytesError（调用端映射 413），不得吞成 badFile 的 400。
func TestDecodeBatches_TrailingMaxBytesPassthrough(t *testing.T) {
	const raw = `[{"id":"1","version":1}]`
	for _, tail := range []string{` {"id":"2"}`, "x" + strings.Repeat(" ", 32)} {
		limit := int64(len(raw) + 2)
		body := io.NopCloser(strings.NewReader(raw + tail))
		err := DecodeBatches(http.MaxBytesReader(nil, body, limit), 2, func([]repository.Document) error { return nil })
		var mbe *http.MaxBytesError
		require.ErrorAs(t, err, &mbe)
	}
}

// handle 错误中断解码（导入事务回滚路径）。
func TestDecodeBatches_HandleErrorAborts(t *testing.T) {
	raw := encodeN(t, 3)
	err := DecodeBatches(bytes.NewReader(raw), 1, func([]repository.Document) error { return errTest })
	require.ErrorIs(t, err, errTest)
}
