# 数据与校验契约

本页以 `internal/meta/meta.go` 的 `Field` / `SchemaDef`、`internal/validation/schema.go` 的 `GetRules` / `ValidateFields` / `ValidateUniqueFields`、`internal/validation/data.go` 的 `ValidateData` 为准。规则可由 `GET /api/v1/admin/types/rules` 查询；UI不要另造规则副本。

## 类型定义

```json
{"type_name":"asset","fields":[{"name":"code","type":"string","required":true,"sensitive":false,"max_chars":255,"max_list_items":0}],"unique_fields":["code"]}
```

名称规则、系统表名/字段禁用集以rules响应为准。保留字段id/version/status/created_at/updated_at/created_by/updated_by/data不能作为用户Schema键。字段类型严格限int、string、int_list、string_list；required与sensitive即使false也返回。

- max_chars用于string以及string_list中每个元素，按Unicode字符数；0采用255，可配置1–65535。int类不能配置该项。
- max_list_items用于两种列表的项数，0采用100，可配置1–65535；标量不能配置该项。
- 数据未知键422；必填缺失/null为422，optional null表示清值。更新为旧data逐键覆盖patch后全量校验，省略键保留，显式null与省略不同。
- int按JSON数值整数性校验；当前解码走float64，大于2^53的精确整数不是本服务承诺，不能用字符串主键规则推导动态int也无损。

unique_fields为1..N个已声明且不重复的标量int/string字段的组合；空数组为无应用唯一性声明。`DataService.checkUniqueDup` 对活跃行查同键，更新/恢复排除自身，命中409；软删行不占用。**第一档限制**：任一键缺失则不查，SELECT→INSERT并发同键仍可能双写；全量导入不做业务唯一键查重，只有文件id重复等校验。当前无partial unique index硬约束，查重可全扫；严格档/存量兼容和实施窗口已由 [R06/P7平台集成合同](../design/platform-integration.md)承接，尚未实现。

## Schema与文档版本

Schema演进请求是完整fields+unique_fields定义及当前metadata version，非字段merge；并发后到者409，重读后重提。去掉unique_fields等于提交空声明，调用方必须理解全量替换语义。兼容变更零数据迁移；破坏性变更在数据下次写时懒校验，原Mongo多版本算法不适用。

数据文档结构含id（JSON字符串）、version（数值revision）、status（active/deleted）、data、created_by/updated_by和时间戳。revision由数据库持久 `_al_document_revision_seq` 分配，正整数可跳号；不同表共享，所有新写、软删/恢复/导入都使用新revision。文件自带version不复用，空导入与ID重用也不能重置该序列。更新必须携带最近读到的version，冲突409；不得客户端+1。

原地升级和恢复时须包含序列状态、停止旧writer；恢复旧备份、down/up重建序列等不提供跨数据库世代，必须隔离旧缓存并要求重读。详见 [升级和部署](../ops/deployment.md)。

## 导入导出与并发

导出含active与deleted完整文档，为裸JSON数组；导出过程中失败可能只截断响应，客户端须验证完整JSON，不能仅看HTTP200。前置失败仍用标准信封，不追加信封到已发文件流。

导入必须是同构JSON数组，正整数id字符串、status限active/deleted；保留源id、时间和操作者，缺省值由导入规则补足。一个事务内表锁→DELETE全表（含软删，不可从本表找回）→流式分批校验插入→ID序列校准。任一错误回滚数据；普通写和导入都使用事务级5秒lock_timeout，锁等待55P03映射409。配置statement_timeout若更短可能先触发其他错误，不是无限等待保证。

同一文件重复导入的业务数据相同，revision不同；导入回包为rows/deleted/max_id（字符串）/duration_ms。业务唯一键不替代本次全量替换。导入前备份、停旧写者和容量要求见 [运维](../ops/README.md)。
