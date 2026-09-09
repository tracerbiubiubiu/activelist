// Package transfer 导入导出的流式编解码（M-A5）。文件本体 = 裸 JSON 数组
// （实现拍板：信封包文件体会破坏流式写出与导出/导入的对称性；导入的 HTTP
// 响应仍走 §6.8 信封）。全部逐条/分批处理、常数内存——百万行级文件不整包
// 载入（§7 导入大文件注意点）。
package transfer

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
)

func badFile(detail string) *apperr.Error {
	return apperr.New(400, apperr.CodeValidation, "导入文件须为 JSON 数组（"+detail+"）")
}

// EncodeDocs 将文档拉取函数写为 JSON 数组（流式：逐条编码）。
// next 返回 io.EOF 表示遍历结束；其余错误中断写出（调用方决定已写出前缀的处置）。
func EncodeDocs(w io.Writer, next func() (*repository.Document, error)) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	first := true
	enc := json.NewEncoder(w)
	for {
		doc, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		// Encoder 每条后带换行——数组内逗号间的空白，合法 JSON
		if err := enc.Encode(doc); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

// DecodeBatches 流式解码 JSON 数组，按 batchSize 分批回调 handle（末批可不满）。
// 任一批返回错误即中断（导入场景 = 事务回滚）。行数超大批次靠 More/Token 驱动，
// 解码器内部仅持有当前批。
func DecodeBatches(r io.Reader, batchSize int, handle func([]repository.Document) error) error {
	dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	tok, err := dec.Token()
	if err != nil {
		return badFile("解析失败: " + err.Error())
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return badFile("首元素非数组")
	}
	for dec.More() {
		batch := make([]repository.Document, 0, batchSize)
		for len(batch) < batchSize && dec.More() {
			var doc repository.Document
			if err := dec.Decode(&doc); err != nil {
				return badFile("行解析失败: " + err.Error())
			}
			batch = append(batch, doc)
		}
		if err := handle(batch); err != nil {
			return err
		}
	}
	if tok, err = dec.Token(); err != nil {
		return badFile("解析失败: " + err.Error())
	}
	if d, ok := tok.(json.Delim); !ok || d != ']' {
		return badFile("数组未闭合")
	}
	return nil
}
