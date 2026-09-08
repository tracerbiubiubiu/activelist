// 每类型动态表管理（M-A2/M-A3）。表形 = 保留列族 + data JSONB（零 DDL 演进，
// 字段全部收在 data 内）；keyset 分页索引 (created_at DESC, id DESC) 随建表同创。
// 表名经 validation 白名单校验后才可到达本包——DDL 内仍统一双引号引用，双保险。
package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

// CreateTableIfNotExists 建类型数据表（注册事务内调用；幂等——
// 元数据行存在而表缺失的自愈路径也走这里，M-A3 数据写入前同样兜底）。
func CreateTableIfNotExists(ctx context.Context, tx pgx.Tx, typeName string) error {
	create := `CREATE TABLE IF NOT EXISTS "` + typeName + `" (
		id         BIGSERIAL PRIMARY KEY,
		version    BIGINT NOT NULL DEFAULT 1,
		status     VARCHAR(16) NOT NULL DEFAULT 'active',
		data       JSONB NOT NULL,
		created_by VARCHAR(64) NOT NULL DEFAULT 'system',
		updated_by VARCHAR(64) NOT NULL DEFAULT 'system',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`
	if _, err := tx.Exec(ctx, create); err != nil {
		return apperr.New(500, apperr.CodeInternal, "创建类型数据表失败")
	}
	index := `CREATE INDEX IF NOT EXISTS "idx_` + typeName + `_created"
		ON "` + typeName + `" (created_at DESC, id DESC)`
	if _, err := tx.Exec(ctx, index); err != nil {
		return apperr.New(500, apperr.CodeInternal, "创建类型数据表索引失败")
	}
	return nil
}
