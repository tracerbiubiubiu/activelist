// 类型数据表 CRUD（M-A3）。表名永不取自路由参数——service 层从元数据行读出的
// typeName 才能到达本包（注册期已过白名单），SQL 内仍统一双引号引用，双保险。
// 事务语义：GetDocForUpdate / UpdateDocData / UpdateDocStatus 必须在 service 开启的
// 同一事务内使用（读-合并-校验-写同锁）；单语句函数可传 pool——dbtx 最小接口
// 使 *pgxpool.Pool 与 pgx.Tx 共用同一套函数（pgx.Tx 天然满足，无需 *Tx 变体）。
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

// 数据行 status 两态（与 meta.StatusActive/Deprecated 的「类型状态」相互独立：
// 类型废弃不改变存量行状态，只挡新写入）。
const (
	StatusActive  = "active"
	StatusDeleted = "deleted"
)

// dbtx 数据通道最小接口（*pgxpool.Pool 与 pgx.Tx 均满足）。
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Document 数据行完整文档。写接口统一返回变更后完整文档（§4——审计契约素材，
// zhuzhao 侧凭响应即可记审计，无需二次查询）。
type Document struct {
	ID        int64          `json:"id"`
	Version   int64          `json:"version"`
	Status    string         `json:"status"` // active | deleted（软删）
	Data      map[string]any `json:"data"`
	CreatedBy string         `json:"created_by"`
	UpdatedBy string         `json:"updated_by"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Cursor keyset 复合游标：完整排序键 (created_at, id) 比较（§7 审计修正——
// 单 id 游标在 id 序 ≠ created_at 序时跳行/重行，导入保留源 id 后必然失序）。
type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

const docCols = `id, version, status, data, created_by, updated_by, created_at, updated_at`

// scanDoc 行 → Document（data JSONB → []byte → map；解析失败=行数据被外部污染，
// 报内部错误而非静默丢字段）。pgx.Rows 满足 pgx.Row 接口，列表迭代复用。
func scanDoc(row pgx.Row) (*Document, error) {
	var d Document
	var raw []byte
	if err := row.Scan(&d.ID, &d.Version, &d.Status, &raw,
		&d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &d.Data); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "data 列解析失败")
	}
	return &d, nil
}

func notFound() *apperr.Error {
	return apperr.New(404, apperr.CodeDataNotFound, "数据不存在")
}

// InsertDoc 插入数据行（version 落列默认 1；operator 空串 COALESCE 回退 'system'
// ——显式 NULL 不触发列 DEFAULT，会 23502，M-A2 复查教训同款）。
func InsertDoc(ctx context.Context, q dbtx, typeName string, data []byte, operator string) (*Document, error) {
	d, err := scanDoc(q.QueryRow(ctx,
		`INSERT INTO "`+typeName+`" (data, created_by, updated_by)
		 VALUES ($1, COALESCE(NULLIF($2, ''), 'system'), COALESCE(NULLIF($2, ''), 'system'))
		 RETURNING `+docCols, data, operator))
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "插入数据失败")
	}
	return d, nil
}

// GetDocByID 单查（不过滤 status：软删行按 id 可见——审计对账与恢复操作入口，§7）。
func GetDocByID(ctx context.Context, q dbtx, typeName string, id int64) (*Document, error) {
	d, err := scanDoc(q.QueryRow(ctx,
		`SELECT `+docCols+` FROM "`+typeName+`" WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "查询数据失败")
	}
	return d, nil
}

// GetDocForUpdate 锁行读（须在事务内）。软删行同样可锁——更新拦截、恢复放行
// 的状态判断由 service 基于返回的 Document 做。
func GetDocForUpdate(ctx context.Context, q dbtx, typeName string, id int64) (*Document, error) {
	d, err := scanDoc(q.QueryRow(ctx,
		`SELECT `+docCols+` FROM "`+typeName+`" WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "锁定数据行失败")
	}
	return d, nil
}

// UpdateDocData 内容更新（乐观锁：version 不匹配 0 行 → 409）。行锁在手的
// 前提下 version 谓词是双保险，防御未来绕过 GetDocForUpdate 的调用方。
func UpdateDocData(ctx context.Context, q dbtx, typeName string, id, expectedVersion int64, data []byte, operator string) (*Document, error) {
	d, err := scanDoc(q.QueryRow(ctx,
		`UPDATE "`+typeName+`" SET data = $3, version = version + 1,
		 updated_by = COALESCE(NULLIF($4, ''), 'system'), updated_at = NOW()
		 WHERE id = $1 AND version = $2
		 RETURNING `+docCols, id, expectedVersion, data, operator))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(409, apperr.CodeConflict, "版本不匹配（数据已被并发修改）").
			WithDetail("expected_version", expectedVersion)
	}
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "更新数据失败")
	}
	return d, nil
}

// UpdateDocStatus 状态迁移（软删/恢复共用；version 随迁移 +1，客户端持有的
// 旧文档立即过期，乐观锁语义对状态变更同样成立）。0 行 = 状态已非 fromStatus，
// moved=false 由 service 决定幂等语义（重复软删/重复恢复返回现态）。
func UpdateDocStatus(ctx context.Context, q dbtx, typeName string, id int64, fromStatus, toStatus, operator string) (*Document, bool, error) {
	d, err := scanDoc(q.QueryRow(ctx,
		`UPDATE "`+typeName+`" SET status = $3, version = version + 1,
		 updated_by = COALESCE(NULLIF($4, ''), 'system'), updated_at = NOW()
		 WHERE id = $1 AND status = $2
		 RETURNING `+docCols, id, fromStatus, toStatus, operator))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, apperr.New(500, apperr.CodeInternal, "更新数据状态失败")
	}
	return d, true, nil
}

// ListDocs 列表：仅 active 行（软删默认排除）、created_at DESC + id DESC、
// keyset 游标按完整排序键比较、LIMIT 硬上限（offset 深翻页在百万行下不可用，不提供）。
func ListDocs(ctx context.Context, q dbtx, typeName string, cur *Cursor, limit int) ([]Document, error) {
	sql := `SELECT ` + docCols + ` FROM "` + typeName + `" WHERE status = $1`
	args := []any{StatusActive}
	if cur != nil {
		sql += ` AND (created_at, id) < ($2, $3)`
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "查询数据列表失败")
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "迭代数据列表失败")
	}
	return out, nil
}
