// 类型管理编排（M-A2）：校验 → [事务：元数据落库 + 动态建表 + 变更历史] 原子提交。
// operator = X-Operator 断言（M-A6 中间件注入；当前调用方传 "system"）。
package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/validation"
)

// RegisterInput 注册请求（POST /api/v1/admin/types body）。
type RegisterInput struct {
	TypeName string       `json:"type_name"`
	Fields   []meta.Field `json:"fields"`
}

// TypeService 类型管理服务。
type TypeService struct {
	pool *pgxpool.Pool
}

func NewTypeService(pool *pgxpool.Pool) *TypeService { return &TypeService{pool: pool} }

// Register 注册类型：白名单校验 → 单事务（元数据 + 动态建表 + 历史）原子提交。
// 重复注册 409；非法输入 422（A1）。
func (s *TypeService) Register(ctx context.Context, in RegisterInput, operator string) (*meta.Definition, error) {
	if err := validation.ValidateTypeName(in.TypeName); err != nil {
		return nil, err
	}
	if err := validation.ValidateFields(in.Fields); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := meta.InsertType(ctx, tx, in.TypeName, in.Fields, operator); err != nil {
		return nil, err
	}
	if err := repository.CreateTableIfNotExists(ctx, tx, in.TypeName); err != nil {
		return nil, err
	}
	if err := meta.InsertHistory(ctx, tx, in.TypeName, "register", in.Fields, operator); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交注册事务失败")
	}
	return meta.GetByName(ctx, s.pool, in.TypeName)
}

// Get 查类型当前 schema 定义。
func (s *TypeService) Get(ctx context.Context, typeName string) (*meta.Definition, error) {
	return meta.GetByName(ctx, s.pool, typeName)
}

// List 类型列表。
func (s *TypeService) List(ctx context.Context) ([]meta.Definition, error) {
	return meta.List(ctx, s.pool)
}

// Deprecate 废弃类型（幂等：已废弃直接返回现态；不存在 404）。
// 迁移发生时落历史（op=deprecate）。
func (s *TypeService) Deprecate(ctx context.Context, typeName, operator string) (*meta.Definition, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 旧态读走 tx（与写同快照）：M-A4 schema 演进上线后，pool 读会拿到演进前
	// 的 fields 进历史；pgx.Tx 满足 meta.pgxPool 接口，无需专用 GetByNameTx。
	cur, err := meta.GetByName(ctx, tx, typeName)
	if err != nil {
		return nil, err
	}
	if cur.Status == meta.StatusActive {
		moved, err := meta.Deprecate(ctx, tx, typeName, operator)
		if err != nil {
			return nil, err
		}
		if moved {
			if err := meta.InsertHistory(ctx, tx, typeName, "deprecate", cur.Fields, operator); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, apperr.New(500, apperr.CodeInternal, "提交废弃事务失败")
		}
	}
	return meta.GetByName(ctx, s.pool, typeName)
}
