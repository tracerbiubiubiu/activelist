// 数据 CRUD 编排（M-A3）：类型存在性/状态 gate → 数据校验 → repository 落库。
// 更新 = 读-合并-全量校验-乐观锁（§1.3 方案 D 懒执行语义）：行锁内取旧行 data、
// 按 body 逐键覆盖合并、对当前 schema 全量校验、version 匹配才写。
// operator = X-Operator 断言（M-A6 中间件注入；当前调用方传 "system"）。
package service

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/validation"
)

// DataService 数据 CRUD 服务（分页参数来自 config business 段）。
type DataService struct {
	pool            *pgxpool.Pool
	pageSizeDefault int
	pageSizeMax     int
}

func NewDataService(pool *pgxpool.Pool, pageSizeDefault, pageSizeMax int) *DataService {
	return &DataService{pool: pool, pageSizeDefault: pageSizeDefault, pageSizeMax: pageSizeMax}
}

// PageLimits 分页钳制参数（handler 解析 query 时消费）。
func (s *DataService) PageLimits() (defaultSize, maxSize int) {
	return s.pageSizeDefault, s.pageSizeMax
}

// InsertInput 插入请求体（POST /api/v1/data/:typeName）。信封包装使 data 与
// 保留列族在 JSON 形态上隔离，未来加元字段（如幂等键）不破契约。
type InsertInput struct {
	Data map[string]any `json:"data"`
}

// UpdateInput 更新请求体（PUT，body 携带 version 乐观锁）。
// Version 绑定层 required（缺省 0 → 400）；service 侧 version<1 守卫兜底直调方。
type UpdateInput struct {
	Data    map[string]any `json:"data"`
	Version int64          `json:"version" binding:"required"`
}

// gateType 数据端点公共前置：类型存在（404）；写类操作对 deprecated 类型拒绝
// （409 TYPE_DEPRECATED，A1 收尾——「deprecated 拒绝写入」= 插入/更新等内容写；
// 软删/恢复是存量数据生命周期操作，允许清理废弃类型的遗留数据）。
// 返回元数据定义：表名永远取自元数据行而非路由参数（repository 层注入防线）。
// check-then-write 的废弃并发窗口不设防：§7 已定性 schema 陈旧窗口为预期行为。
func (s *DataService) gateType(ctx context.Context, typeName string, forWrite bool) (*meta.Definition, error) {
	def, err := meta.GetByName(ctx, s.pool, typeName)
	if err != nil {
		return nil, err
	}
	if forWrite && def.Status == meta.StatusDeprecated {
		return nil, apperr.New(409, apperr.CodeTypeDepr, "类型已废弃，拒绝写入: "+typeName).
			WithDetail("type_name", typeName)
	}
	return def, nil
}

// Insert 插入数据（A2）。校验对请求原文进行——插入没有「旧行」可合并。
func (s *DataService) Insert(ctx context.Context, typeName string, in InsertInput, operator string) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, true)
	if err != nil {
		return nil, err
	}
	if err := validation.ValidateData(def.Fields, in.Data); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in.Data)
	if err != nil {
		return nil, apperr.New(422, apperr.CodeValidation, "data 序列化失败")
	}
	return repository.InsertDoc(ctx, s.pool, def.TypeName, raw, operator)
}

// Get 单查（软删行可见，status 字段标注现态）。
func (s *DataService) Get(ctx context.Context, typeName string, id int64) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	return repository.GetDocByID(ctx, s.pool, def.TypeName, id)
}

// List 列表（仅 active 行；keyset 游标语义见 repository.Cursor）。
func (s *DataService) List(ctx context.Context, typeName string, cur *repository.Cursor, pageSize int) ([]repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	return repository.ListDocs(ctx, s.pool, def.TypeName, cur, pageSize)
}

// Update 更新数据（读-合并-全量校验-乐观锁；A2/A4）。
func (s *DataService) Update(ctx context.Context, typeName string, id int64, in UpdateInput, operator string) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, true)
	if err != nil {
		return nil, err
	}
	if in.Version < 1 {
		return nil, apperr.New(422, apperr.CodeValidation, "version 必填（乐观锁，取最近一次读到的 version）").
			WithDetail("field", "version")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	doc, err := repository.GetDocForUpdate(ctx, tx, def.TypeName, id)
	if err != nil {
		return nil, err
	}
	if doc.Status != repository.StatusActive {
		return nil, apperr.New(409, apperr.CodeConflict, "数据已软删，须先恢复再更新").
			WithDetail("id", id).WithDetail("status", doc.Status)
	}
	if doc.Version != in.Version {
		return nil, apperr.New(409, apperr.CodeConflict, "版本不匹配（数据已被并发修改）").
			WithDetail("expected_version", in.Version).WithDetail("current_version", doc.Version)
	}

	merged := mergeData(doc.Data, in.Data)
	if err := validation.ValidateData(def.Fields, merged); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, apperr.New(422, apperr.CodeValidation, "data 序列化失败")
	}
	doc, err = repository.UpdateDocData(ctx, tx, def.TypeName, id, in.Version, raw, operator)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交更新事务失败")
	}
	return doc, nil
}

// SoftDelete 软删除（幂等：已删行返回现态不变；A2）。
func (s *DataService) SoftDelete(ctx context.Context, typeName string, id int64, operator string) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	doc, err := repository.GetDocForUpdate(ctx, tx, def.TypeName, id)
	if err != nil {
		return nil, err
	}
	if doc.Status == repository.StatusActive {
		nd, moved, err := repository.UpdateDocStatus(ctx, tx, def.TypeName, id,
			repository.StatusActive, repository.StatusDeleted, operator)
		if err != nil {
			return nil, err
		}
		if moved {
			doc = nd
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交软删事务失败")
	}
	return doc, nil
}

// Restore 恢复软删数据（幂等：active 行返回现态不变；A2「恢复后可写」）。
func (s *DataService) Restore(ctx context.Context, typeName string, id int64, operator string) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	doc, err := repository.GetDocForUpdate(ctx, tx, def.TypeName, id)
	if err != nil {
		return nil, err
	}
	if doc.Status == repository.StatusDeleted {
		nd, moved, err := repository.UpdateDocStatus(ctx, tx, def.TypeName, id,
			repository.StatusDeleted, repository.StatusActive, operator)
		if err != nil {
			return nil, err
		}
		if moved {
			doc = nd
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交恢复事务失败")
	}
	return doc, nil
}

// mergeData 更新合并：旧行 data 为底、body 逐键覆盖（部分更新语义——未提及的
// 键保留；显式 null 覆盖为清值，required 键清值由校验拦截）。
func mergeData(old, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(old)+len(patch))
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range patch {
		merged[k] = v
	}
	return merged
}
