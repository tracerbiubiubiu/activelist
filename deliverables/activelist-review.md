# activelist 方案评审报告

> **⚠️ 历史文档注记（2026-09-03）**：本评审针对**收敛前的原始方案**（MongoDB + 3 进程 + 事件驱动 + 两层审计）。随职责收敛（见 [`activelist.md`](./activelist.md) 头部收敛声明），**事件 / 历史快照 / Asynq / watcher 相关发现已失效**（C2、C3、H4、H5、H7，及第六节中事件处理可靠性、两层审计等评估行）；**数据模型 / 并发 / 校验侧发现仍具参考价值**（C1、H1、H3、M4、M5 与 §5/§6/§10 相关评估）。其中 C1 的 schemaVersion 方案 A/B/C 之辩已由**方案 D 定稿**关闭（2026-09-03，见 activelist.md 头部「设计定稿补充」）。现行实现依据以 [`implementation-plan.md`](./implementation-plan.md) 为准。

> **评审对象**：`docs/soar/activelist.md`（2035 行，22 章节）
>
> **评审范围**：仅评审 activelist 新模块**自身设计**的全面性与能力达成度。§19"与 zhuzhao 集成"作为 activelist 项目的工作清单，其完整性由实施团队对齐，不纳入本评审核心。
>
> **评审结论**：方案整体成熟可行，覆盖面广。3 个 CRITICAL 级问题已修订（方案 B / 降级承诺 / 心跳TTL），2 处待业务方确认。

## 一、总体结论

| 维度 | 评级 | 说明 |
|------|------|------|
| 全面性 | ★★★★☆ | 22 章节覆盖架构/数据/并发/可靠性/容量/测试，缺备份恢复/限流/runbook |
| 内部一致性 | ★★★☆☆ | §5.4 vs §6.4、§7.2 vs §12 存在设计矛盾 |
| 能力达成 | ★★★★☆ | 9/10 项承诺能力可达成，"事件不丢失"需降级 |

## 二、CRITICAL 级问题（3 项，已修订）

### C1. §5.4 vs §6.4 "向前兼容"承诺自相矛盾 — ✅ 已修订（方案 B，待业务方确认）

**原问题**：§5.4 算法对所有新版字段（含 optional）返回 422，与 §6.4 "向前兼容、零停机演进"承诺矛盾。

**修订内容**（§5.4 line 287-360）：
- 新增"设计决策：schemaVersion 语义"小节，列出方案 A/B/C，标记方案 B 为推荐
- 主体算法按方案 B 重写：Update 允许新版 optional 字段（schemaVersion 不变），仅拒绝新版 required 字段
- 联动修订 §6.2（Update 行）、§6.4（向前兼容定义）、§10.2（乐观锁伪代码）、§11.3（协同规则）
- 关键决策汇总表新增 3 行（Update 校验 / schemaVersion 语义 / Update 改新版字段）

**待业务方确认**：schemaVersion 语义是否采用方案 B（创建时版本，Update 不改变）。方案 B 对标 Confluent Schema Registry。

### C2. §7.2 vs §12 "事件不丢失"承诺与 oplog 过期矛盾 — ✅ 已修订

**原问题**：§12 承诺"事件不丢失"，§7.2 承认 oplog 过期时"接受事件丢失"。

**修订内容**：
- §12 line 1137：`事件不丢失` → `事件不丢失（oplog 窗口内）`
- §22 line 1994：总结同步修订
- §16.3：补充 `oplog 过期导致 token 失效` P0 告警 + `事件丢失审计` P0 告警
- 关键决策汇总表新增 `事件可靠性承诺` 行
- **不补充恢复机制**（history 也可能丢 + delete 事件无 snapshot + 全表扫描不可行，技术限制）

### C3. §6.6 迁移互斥机制无心跳/崩溃恢复 — ✅ 已修订

**原问题**：迁移工具崩溃后 `migrate_progress` 永远停留 `running`，apiserver 永久拒绝 Schema 变更。

**修订内容**（§6.6 line 575-595）：
- 迁移工具每 30s 更新 `heartbeat` 字段
- apiserver 检查 `heartbeat` 超时 5min → 自动判定僵尸迁移，`status` 改为 `abandoned`，允许 Schema 变更 + P1 告警
- 迁移工具重启时检查 `abandoned` 状态，需 `--force` 确认
- 提供 `cmd/migrate cleanup` 兜底工具
- 对标 Flyway `repair` / etcd lease 过期自动释放
- 关键决策汇总表更新 `迁移与 Schema 变更` 行

## 三、HIGH 级问题（8 项，需补救）

| # | 位置 | 问题与建议 |
|---|------|-----------|
| H1 | §6.7 line 552 | 响应含 `total`，大集合下 `CountDocuments` 慢。建议补充 `estimatedDocumentCount` 或缓存 total |
| H2 | §6.3 line 436-439 | Schema 缓存 60s TTL，多 apiserver 实例间 60s 不一致。应明确这 60s 窗口内 Insert 可能用旧 Schema 校验后写入（schemaVersion 标记为旧版——合法但需说明） |
| H3 | §6.6 line 508 | "失败可重试（幂等）"但未描述机制：重试时如何避免重复迁移单条数据？迁移到一半失败如何处理？应明确单文档迁移用条件 `filter: {schemaVersion: from}` 保证幂等 |
| H4 | §7.4 line 872-880 | WriteFallback 成功后、SaveResumeToken 之前崩溃 → 重启用旧 token → 同一事件再次 WriteFallback → fallback 表产生重复记录。fallback 表无 `resumeTokenHash` 唯一索引。worker 端有幂等兜底，但 fallback 表会膨胀。建议 fallback 表加 `{resumeTokenHash: 1}` 唯一索引 |
| H5 | §7.5 line 906-908 | Watcher 多副本选主"Redlock 或 findAndModify"二选一未定。正式环境方案应明确选定并给实现细节 |
| H6 | §17.2 line 1447-1452 | 容量规划单一假设（100 类型 × 10万 × 5次/天），对 IoT/日志类场景严重偏低。建议按业务类型分级估算框架 |
| H7 | §5.3 line 248-255 | `resumeToken` 原文存储仅供调试，但可能几 KB，历史集合膨胀后浪费空间。建议只存 hash，调试时从 `resume_tokens` 集合反查 |
| H8 | §19.2 line 1623 | Restrict 资源 `activelist` 注册了，但未说哪些角色获得哪些 action grants。属实施工作清单，建议在 §19 补充"adminPreset 自动获得全权限"等说明 |

## 四、MEDIUM 级问题（8 项，细节改进）

| # | 位置 | 问题 |
|---|------|------|
| M1 | §6.8 | `DEPENDENCY_UNAVAILABLE` 与 HTTP 503 的 `detail.error_code` 对应关系未明示（⚠️ 2026-09-16 注：detail.error_code 概念随 2026-09-08 信封收敛消亡，本条前提已不成立） |
| M2 | §15.1 | activelist 内部组件（worker/watcher）访问 Mongo/Redis 的认证方式未说 |
| M3 | §19.7.4 | accesslog 层查 Schema 脱敏的性能开销未评估（每次请求查一次 Schema） |
| M4 | §6.2 | 并发 Insert 同一自定义 `_id` 的冲突响应未明确 |
| M5 | §10.3 | 乐观锁重试 3 次失败的降级方案缺失（高并发场景） |
| M6 | 全文 | 缺 API 限流设计（429 错误码提到但无策略） |
| M7 | §8.1 | 业务数据集合 `col_<typeName>` 和历史集合的备份恢复策略缺失（仅备份 schema_definitions） |
| M8 | §13 | 缺"Schema 版本归档"流程示例（§14 提到但无示例） |

## 五、LOW 级问题（5 项，建议性）

| # | 问题 |
|---|------|
| L1 | §4 技术栈未列依赖注入方案（wire 还是手写 DI） |
| L2 | §20 测试策略缺性能基准（QPS/延迟目标） |
| L3 | §5.4 伪代码用 Python 风格，与 Go 项目不符 |
| L4 | 缺运维 runbook（扩容/升级/回滚步骤） |
| L5 | 缺 `cmd/cleanup` 工具的具体设计（§11.1 提到但未展开） |

## 六、能力达成评估

| 承诺能力（§22） | 评估 | 说明 |
|---------|------|------|
| 无需重启扩展 | ✅ 可达成 | 动态 Schema + 集合懒加载 |
| 写入质量可控 | ✅ 可达成 | gojsonschema + 字段白名单 |
| 查询性能高效 | ⚠️ 基本达成 | 物理隔离好，通配符索引有限制需手动建索引 |
| 事件处理可靠 | ⚠️ 有条件达成 | "事件不丢失"需降级为"oplog 窗口内不丢失"（C2） |
| 数据全生命周期追溯 | ✅ 可达成 | 历史集合永久保留 + 幂等保证 |
| Schema 全生命周期管理 | ✅ 可达成 | 版本化 + 归档 + 不可删除 |
| 基础设施高可用 | ✅ 可达成 | Mongo 副本集 + Redis fallback |
| 并发安全 | ✅ 可达成 | 乐观锁 + FindOneAndUpdate |
| 两层审计日志 | ✅ 可达成 | 设计合理 |

## 七、建议的修订优先级

1. **C1**（§5.4 vs §6.4 矛盾）—— 最影响业务可用性，必须先决策可选字段语义
2. **C2**（事件不丢失承诺降级）—— 承诺与实现对齐
3. **C3**（迁移互斥机制加心跳）—— 运维可靠性
4. **H1-H8** —— 逐项补救
5. **M1-M8** —— 细节打磨

**3 个 CRITICAL 问题中，C1 最影响业务可用性（决定向前兼容的真实语义），C2 影响可靠性承诺的可信度，C3 影响运维可恢复性。** 三者都需在实施前明确决策，否则方案落地后会出现设计预期与实际行为不符的问题。
