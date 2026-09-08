// Package meta 类型元数据读写（data_types + data_type_schema_history，000001）。
// 事务语义：注册/废弃由 service 层开事务并传入 pgx.Tx——元数据、动态建表 DDL、
// 变更历史三者同事务原子提交（PG 支持事务化 DDL，这是选 PG 的红利之一）。
package meta

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

// Field 用户字段定义（最终画像收窄版）。Sensitive 为日志脱敏钩子预留标记
// （M-A6 消费，当前仅存储）。
type Field struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // int | string | int_list | string_list
	Required  bool   `json:"required"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// SchemaDef 当前 schema（对象包装，向前可扩展）。
type SchemaDef struct {
	Fields []Field `json:"fields"`
}

// Definition 类型定义文档（data_types 行 + 解析后的 schema）。
type Definition struct {
	TypeName  string    `json:"type_name"`
	Fields    []Field   `json:"fields"`
	Status    string    `json:"status"` // active | deprecated
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	StatusActive     = "active"
	StatusDeprecated = "deprecated"
)

// pgxPool 只读通道最小接口（*pgxpool.Pool 满足；解耦具体类型便于测试替身）。
type pgxPool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// schemaDefJSON 列存储形态（与 Definition.Fields 同源，单点序列化）。
func schemaDefJSON(fields []Field) ([]byte, error) {
	return json.Marshal(SchemaDef{Fields: fields})
}

// InsertType 注册类型（事务内）。typeName 唯一冲突 → 409 TYPE_ALREADY_EXISTS。
// created_by/updated_by 为 X-Operator 断言（M-A6 起真实值，当前 system）；
// 空串经 COALESCE 回退列默认 'system'——显式 NULL 不触发列 DEFAULT，会 23502。
func InsertType(ctx context.Context, tx pgx.Tx, typeName string, fields []Field, operator string) error {
	def, err := schemaDefJSON(fields)
	if err != nil {
		return apperr.New(500, apperr.CodeInternal, "schema 序列化失败")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO data_types (type_name, schema_def, status, created_by, updated_by)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'system'), COALESCE(NULLIF($4, ''), 'system'))`,
		typeName, def, StatusActive, operator)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.New(409, apperr.CodeTypeExists, "类型已存在: "+typeName).
				WithDetail("type_name", typeName)
		}
		return apperr.New(500, apperr.CodeInternal, "注册类型失败")
	}
	return nil
}

// InsertHistory 追加变更历史（事务内；只追加不更新）。
func InsertHistory(ctx context.Context, tx pgx.Tx, typeName, op string, fields []Field, operator string) error {
	def, err := schemaDefJSON(fields)
	if err != nil {
		return apperr.New(500, apperr.CodeInternal, "schema 序列化失败")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO data_type_schema_history (type_name, op, schema_def, changed_by)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'system'))`, typeName, op, def, operator)
	if err != nil {
		return apperr.New(500, apperr.CodeInternal, "写入变更历史失败")
	}
	return nil
}

// scanDef 行 → Definition（schema_def JSONB 解析失败=元数据被外部污染，报内部错误）。
func scanDef(row pgx.Row) (*Definition, error) {
	var d Definition
	var raw []byte
	err := row.Scan(&d.TypeName, &raw, &d.Status, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(404, apperr.CodeTypeNotFound, "类型不存在")
	}
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "查询类型失败")
	}
	var sd SchemaDef
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "schema 解析失败")
	}
	d.Fields = sd.Fields
	return &d, nil
}

const selectCols = `type_name, schema_def, status, version, created_at, updated_at`

// GetByName 按名查类型定义（pool 通道）。
func GetByName(ctx context.Context, pool pgxPool, typeName string) (*Definition, error) {
	return scanDef(pool.QueryRow(ctx,
		`SELECT `+selectCols+` FROM data_types WHERE type_name = $1`, typeName))
}

// List 全部类型（类型数量为小量级，无分页；按创建时间倒序）。
func List(ctx context.Context, pool pgxPool) ([]Definition, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+selectCols+` FROM data_types ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "查询类型列表失败")
	}
	defer rows.Close()
	var out []Definition
	for rows.Next() {
		d, err := scanDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// Deprecate 废弃类型（事务内）：active → deprecated 才产生状态迁移；
// 不存在由调用方先 GetByName 判定（本函数返回迁移是否发生）。
func Deprecate(ctx context.Context, tx pgx.Tx, typeName, operator string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE data_types SET status = $2, updated_by = COALESCE(NULLIF($3, ''), 'system'), updated_at = NOW()
		WHERE type_name = $1 AND status = $4`,
		typeName, StatusDeprecated, operator, StatusActive)
	if err != nil {
		return false, apperr.New(500, apperr.CodeInternal, "废弃类型失败")
	}
	return tag.RowsAffected() > 0, nil
}
