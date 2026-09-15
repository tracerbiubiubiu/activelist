// 数据 CRUD 编排（M-A3）：类型存在性/状态 gate → 数据校验 → repository 落库。
// 更新 = 读-合并-全量校验-乐观锁（§1.3 方案 D 懒执行语义）：行锁内取旧行 data、
// 按 body 逐键覆盖合并、对当前 schema 全量校验、version 匹配才写。
// operator = X-Operator 断言（M-A6 中间件注入；当前调用方传 "system"）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
	"github.com/tracerbiubiubiu/activelist/internal/transfer"
	"github.com/tracerbiubiubiu/activelist/internal/validation"
)

// DataService 数据 CRUD 服务（分页/导入分批与 body 上限参数来自 config business 段）。
type DataService struct {
	pool            *pgxpool.Pool
	pageSizeDefault int
	pageSizeMax     int
	importBatchRows int
	importMaxBytes  int64
}

func NewDataService(pool *pgxpool.Pool, pageSizeDefault, pageSizeMax, importBatchRows int, importMaxBytes int64) *DataService {
	return &DataService{pool: pool, pageSizeDefault: pageSizeDefault, pageSizeMax: pageSizeMax, importBatchRows: importBatchRows, importMaxBytes: importMaxBytes}
}

// PageLimits 分页钳制参数（handler 解析 query 时消费）。
func (s *DataService) PageLimits() (defaultSize, maxSize int) {
	return s.pageSizeDefault, s.pageSizeMax
}

// ImportMaxBytes 导入 body 上限（handler 挂 MaxBytesReader 时消费）。
func (s *DataService) ImportMaxBytes() int64 { return s.importMaxBytes }

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
		return nil, apperr.New(409, apperr.CodeTypeDepr,
			"类型已废弃，拒绝内容写入: "+typeName+
				"（仅允许查询、导出及软删/恢复存量数据）").
			WithDetail("type_name", typeName).WithDetail("current_status", def.Status)
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
		return nil, s.mapDataValidationError(ctx, err, def, false)
	}
	raw, err := json.Marshal(in.Data)
	if err != nil {
		return nil, apperr.New(422, apperr.CodeValidation, "data 序列化失败")
	}
	// 插入也走事务挂 lock_timeout（§7：导入长事务期间常规写 5s 快速失败 409）
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}
	doc, err := repository.InsertDoc(ctx, tx, def.TypeName, raw, operator)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交插入事务失败")
	}
	return doc, nil
}

// Get 单查（软删行可见，status 字段标注现态）。
func (s *DataService) Get(ctx context.Context, typeName string, id int64) (*repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	return repository.GetDocByID(ctx, s.pool, def.TypeName, id)
}

// List 列表（仅 active 行；keyset 游标语义见 repository.Cursor）。limit 在本层
// 再钳制一次——PG `LIMIT -1` 语义为不限行，直调方传非正值不得放大查询。
func (s *DataService) List(ctx context.Context, typeName string, cur *repository.Cursor, pageSize int) ([]repository.Document, error) {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return nil, err
	}
	if pageSize < 1 {
		pageSize = s.pageSizeDefault
	}
	if pageSize > s.pageSizeMax {
		pageSize = s.pageSizeMax
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
	if err := setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}

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
		return nil, s.mapDataValidationError(ctx, err, def, true)
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
	if err := setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}

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
	if err := setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}

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

// mapDataValidationError 数据写路径校验错误 → 懒执行契约分档（§4：FIELD_DEPRECATED /
// NEW_REQUIRED_FIELD 迁移提示）。validation 返回的 reason 分档：
//   - missing_required：更新路径 = 旧数据未含演进新增必填字段 → NEW_REQUIRED_FIELD
//     （错误信息含迁移指引，A3）；插入路径保持 VALIDATION_ERROR（新建数据本就该全量给齐）。
//   - unknown_field：查字段史——曾存在于历史 schema = 已移除字段待清理 →
//     FIELD_DEPRECATED；否则维持 VALIDATION_ERROR（真拼写错误）。
func (s *DataService) mapDataValidationError(ctx context.Context, err error, def *meta.Definition, isUpdate bool) error {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return err
	}
	field, _ := ae.Detail["field"].(string)
	switch ae.Detail["reason"] {
	case "missing_required":
		if !isUpdate {
			return ae
		}
		return apperr.New(422, apperr.CodeNewRequired,
			"缺少必填字段: "+field+"——旧数据未含 schema 演进新增的必填字段，须补齐该字段后方可更新（或经导入全量重灌）").
			WithDetail("field", field)
	case "unknown_field":
		existed, herr := meta.FieldExistedInHistory(ctx, s.pool, def.TypeName, field)
		if herr != nil {
			return herr
		}
		if existed {
			return apperr.New(422, apperr.CodeFieldDepr,
				"字段已从 schema 移除: "+field+"——旧数据须移除该字段后方可写入（或经导入全量重灌）").
				WithDetail("field", field)
		}
		return ae
	default:
		return ae
	}
}

// setLockTimeout 常规写事务 5s 锁等待上限（§7：导入长事务持锁期间，常规写快速
// 失败 409 而非挂满连接池；SET LOCAL 仅作用于当前事务，连接池无状态泄漏）。
func setLockTimeout(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return apperr.New(500, apperr.CodeInternal, "设置锁等待超时失败")
	}
	return nil
}

// ImportResult 全量替换导入的批次汇总（§4：行数/耗时/max id——审计契约素材）。
type ImportResult struct {
	Rows       int64 `json:"rows"`        // 重灌行数
	Deleted    int64 `json:"deleted"`     // 清表删除行数（含软删）
	MaxID      int64 `json:"max_id"`      // 导入后最大 id（序列已校准至 max+1）
	DurationMS int64 `json:"duration_ms"` // 事务内全程耗时
}

// Export 全量导出（流式写 JSON 数组到 w；含软删行；含 id/status/created_at 等
// 完整文档字段——A5 导出→导入闭环）。gate 过后才开始写，前置错误可安全转为
// 业务错误响应；流中途错误只能截断（已在响应头之后，HTTP 层不可回滚）。
func (s *DataService) Export(ctx context.Context, typeName string, w io.Writer) error {
	def, err := s.gateType(ctx, typeName, false)
	if err != nil {
		return err
	}
	next, cancel, err := repository.IterDocsAll(ctx, s.pool, def.TypeName)
	if err != nil {
		return err
	}
	defer cancel()
	return transfer.EncodeDocs(w, next)
}

// Import 全量替换导入（A5）：单事务内 LOCK TABLE（防并发 INSERT 漏过，§7 审计
// 修正）→ 清表 → 流式分批校验重灌（version 重置 1）→ setval 序列。文件内
// 重复 id 422；逐行对当前 schema 全量校验（写路径全量校验，§1.3）；废弃类型
// 拒绝导入（gate forWrite=true，与插入/更新同口径）。
func (s *DataService) Import(ctx context.Context, typeName string, r io.Reader, operator string) (*ImportResult, error) {
	def, err := s.gateType(ctx, typeName, true)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 导入互斥同样快速失败（审计 M1）：SHARE ROW EXCLUSIVE 会长时持锁，
	// 同类型并发导入的第二等待者若不设超时将无界挂起并占用连接
	if err := setLockTimeout(ctx, tx); err != nil {
		return nil, err
	}

	if err := repository.LockForReplace(ctx, tx, def.TypeName); err != nil {
		return nil, err
	}
	deleted, err := repository.DeleteAllDocs(ctx, tx, def.TypeName)
	if err != nil {
		return nil, err
	}

	res := &ImportResult{Deleted: deleted}
	now := time.Now()
	err = transfer.DecodeBatches(r, s.importBatchRows, func(batch []repository.Document) error {
		for i := range batch {
			d := &batch[i]
			if d.ID <= 0 {
				return apperr.New(422, apperr.CodeValidation, "导入行缺少正整数 id（全量替换保留源 id）")
			}
			if d.Status == "" {
				d.Status = repository.StatusActive
			}
			if d.Status != repository.StatusActive && d.Status != repository.StatusDeleted {
				return apperr.New(422, apperr.CodeValidation, "导入行 status 非法（active|deleted）").
					WithDetail("id", d.ID).WithDetail("status", d.Status)
			}
			if d.CreatedAt.IsZero() {
				d.CreatedAt = now
			}
			if d.UpdatedAt.IsZero() {
				d.UpdatedAt = now
			}
			if err := validation.ValidateData(def.Fields, d.Data); err != nil {
				return apperr.New(422, apperr.CodeValidation, "导入行 schema 校验失败（id="+strconv.FormatInt(d.ID, 10)+"）").
					WithDetail("id", d.ID).WithDetail("cause", err.Error())
			}
		}
		res.Rows += int64(len(batch))
		return repository.InsertImportBatch(ctx, tx, def.TypeName, batch)
	})
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, apperr.New(413, apperr.CodeValidation, "导入文件超过大小上限（import_max_bytes）").
				WithDetail("limit_bytes", mbe.Limit)
		}
		return nil, err
	}
	maxID, err := repository.SetSequenceAfterImport(ctx, tx, def.TypeName)
	if err != nil {
		return nil, err
	}
	res.MaxID = maxID
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.New(500, apperr.CodeInternal, "提交导入事务失败")
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
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
