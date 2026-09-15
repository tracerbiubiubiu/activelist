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

	if err := meta.InsertType(ctx, tx, s.pool, in.TypeName, in.Fields, operator); err != nil {
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

// EvolveInput schema 演进请求体（POST /api/v1/admin/types/:typeName/schema）。
// 全量定义、非字段级 merge（§7）；version 为元数据行乐观锁。
type EvolveInput struct {
	Fields  []meta.Field `json:"fields"`
	Version int64        `json:"version" binding:"required"`
}

// Evolve schema 演进（M-A4；方案 D）：全量定义校验 → [事务：乐观锁替换 + 历史] →
// 提交。兼容变更（加 optional/放宽）零迁移；破坏性变更允许提交、旧数据懒执行——
// 下次数据更新按新 schema 校验 422（NEW_REQUIRED_FIELD / FIELD_DEPRECATED 迁移提示）。
// 废弃类型拒绝演进（终态不再扩 schema）；不存在 404；版本不匹配 409 须重读重提。
func (s *TypeService) Evolve(ctx context.Context, typeName string, in EvolveInput, operator string) (*meta.Definition, error) {
	if err := validation.ValidateFields(in.Fields); err != nil {
		return nil, err
	}
	cur, err := meta.GetByName(ctx, s.pool, typeName)
	if err != nil {
		return nil, err
	}
	if cur.Status == meta.StatusDeprecated {
		return nil, apperr.New(409, apperr.CodeTypeDepr, "类型已废弃，拒绝演进: "+typeName).
			WithDetail("type_name", typeName).WithDetail("current_status", cur.Status)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	moved, err := meta.UpdateSchema(ctx, tx, typeName, in.Fields, in.Version, operator)
	if err != nil {
		return nil, err
	}
	if !moved {
		// CAS 0 行有两种成因（WHERE version=$ AND status='active'）：锁内重读区分，
		// 避免调用方在「重试演进」与「类型已终态」之间无法判断。
		latest, err := meta.GetByName(ctx, tx, typeName)
		if err != nil {
			return nil, err
		}
		if latest.Status == meta.StatusDeprecated {
			return nil, apperr.New(409, apperr.CodeTypeDepr,
				"类型在演进提交前已被并发废弃，拒绝演进: "+typeName).
				WithDetail("type_name", typeName).WithDetail("current_status", latest.Status)
		}
		return nil, apperr.New(409, apperr.CodeConflict,
			"schema 版本不匹配（已被并发演进推进），须重读最新定义后重提").
			WithDetail("type_name", typeName).
			WithDetail("expected_version", in.Version).
			WithDetail("current_version", latest.Version)
	}
	if err := meta.InsertHistory(ctx, tx, typeName, "evolve", in.Fields, operator); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交演进事务失败")
	}
	return meta.GetByName(ctx, s.pool, typeName)
}

// History schema 变更历史（新→旧；类型不存在 404）。
func (s *TypeService) History(ctx context.Context, typeName string) ([]meta.HistoryEntry, error) {
	if _, err := meta.GetByName(ctx, s.pool, typeName); err != nil {
		return nil, err
	}
	return meta.ListHistory(ctx, s.pool, typeName)
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
			// 锁内重读（Deprecate×Evolve 交错修复）：GetByName 的快照读发生在行锁
			// 等待之前——并发演进提交后，历史行会记录演进前 schema（审计失真）。
			// 拿到行锁后重读，才是废弃时刻的真实 schema。
			cur, err = meta.GetByName(ctx, tx, typeName)
			if err != nil {
				return nil, err
			}
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
