// 导入导出的存储原语（M-A5）：全量遍历 / 替换锁 / 清表 / 批量重灌 / 序列校准。
// 表名来源与 data.go 同纪律——service 从元数据行读出，SQL 内双引号引用。
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

// IterDocsAll 全量遍历（含软删行——导出必须带软删，否则导出→导入闭环丢数据，§7），
// id 序保证重灌后文件行序稳定。返回拉取迭代器 + rows 关闭函数。
func IterDocsAll(ctx context.Context, q dbtx, typeName string) (next func() (*Document, error), cancel func(), err error) {
	rows, err := q.Query(ctx,
		`SELECT `+docCols+` FROM "`+typeName+`" ORDER BY id ASC`)
	if err != nil {
		return nil, nil, apperr.New(500, apperr.CodeInternal, fmt.Sprintf("导出查询失败: %v", err))
	}
	next = func() (*Document, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return nil, apperr.New(500, apperr.CodeInternal, fmt.Sprintf("导出迭代失败: %v", err))
			}
			return nil, io.EOF
		}
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		return d, nil
	}
	return next, func() { rows.Close() }, nil
}

// LockForReplace 全量替换前置锁（§7 审计修正：DELETE 只锁既有行，并发 INSERT
// 的新行在替换提交后存活、破坏全量替换语义与 A5 幂等——SHARE ROW EXCLUSIVE
// 阻塞并发写（含其他导入）不阻塞读，兼按类型互斥）。事务级锁，随事务释放。
func LockForReplace(ctx context.Context, q dbtx, typeName string) error {
	if _, err := q.Exec(ctx, `LOCK TABLE "`+typeName+`" IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		if e := errLockWait(err); e != nil {
			return e
		}
		return apperr.New(500, apperr.CodeInternal, fmt.Sprintf("替换锁获取失败: %v", err))
	}
	return nil
}

// DeleteAllDocs 清表（含软删行），返回删除行数（批次审计素材）。
func DeleteAllDocs(ctx context.Context, q dbtx, typeName string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM "`+typeName+`"`)
	if err != nil {
		return 0, apperr.New(500, apperr.CodeInternal, fmt.Sprintf("清表失败: %v", err))
	}
	return tag.RowsAffected(), nil
}

// InsertImportBatch 批量重灌：保留源 id/status/时间戳/操作者，version 一律重置 1
// （方案 D 定稿）。文件内 id 重复 → PK 23505 → 422。
func InsertImportBatch(ctx context.Context, q dbtx, typeName string, docs []Document) error {
	batch := &pgx.Batch{}
	for i := range docs {
		raw, err := json.Marshal(docs[i].Data)
		if err != nil {
			return apperr.New(422, apperr.CodeValidation,
				"导入行 data 序列化失败（id="+strconv.FormatInt(docs[i].ID, 10)+"）")
		}
		batch.Queue(`INSERT INTO "`+typeName+`"
			(id, version, status, data, created_by, updated_by, created_at, updated_at)
			VALUES ($1, 1, $2, $3,
			        COALESCE(NULLIF($4, ''), 'system'), COALESCE(NULLIF($5, ''), 'system'),
			        $6, $7)`,
			docs[i].ID, docs[i].Status, raw,
			docs[i].CreatedBy, docs[i].UpdatedBy, docs[i].CreatedAt, docs[i].UpdatedAt)
	}
	br := q.SendBatch(ctx, batch)
	defer br.Close()
	for i := range docs {
		if _, err := br.Exec(); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// 唯一成因 = 文件内 id 重复（导入先清表 + SHARE ROW EXCLUSIVE 锁阻写，
				// setval 又在最后——不可能与现存行或序列冲突）；回显具体 id 便于定位
				return apperr.New(422, apperr.CodeValidation, "导入文件存在重复 id").
					WithDetail("duplicate_id", docs[i].ID)
			}
			if e := errLockWait(err); e != nil {
				return e
			}
			return apperr.New(500, apperr.CodeInternal, "导入批量写入失败")
		}
	}
	return nil
}

// SetSequenceAfterImport setval 至 max(id)+1（is_called=false，下一个返回值即
// max+1）；空表 setval 1 → 后续插入从 id=1 起。返回 max(id)（批次审计素材）。
func SetSequenceAfterImport(ctx context.Context, q dbtx, typeName string) (int64, error) {
	var next int64
	err := q.QueryRow(ctx,
		`SELECT setval(pg_get_serial_sequence('"`+typeName+`"', 'id'),
		        (SELECT COALESCE(MAX(id), 0) FROM "`+typeName+`") + 1, false)`).Scan(&next)
	if err != nil {
		return 0, apperr.New(500, apperr.CodeInternal, "序列校准失败")
	}
	return next - 1, nil
}

// errLockWait 锁等待超时（55P03 lock_timeout）→ 409：导入长事务期间常规写的
// 快速失败路径（§7——避免请求在行/表锁上挂住耗尽连接池）。
func errLockWait(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return apperr.New(409, apperr.CodeConflict, "数据表被导入长事务锁定（lock_timeout 5s），请稍后重试")
	}
	return nil
}
