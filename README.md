# activelist

动态多类型数据全生命周期管理平台 —— **已收敛为「动态数据模型薄层」**（2026-09-03）。

## 定位

用户可自定义数据类型 / 动态字段（`int`、`string` 及二者的列表）的高可靠数据存储服务。作为**独立服务 + 独立库 + 独立数据库**部署，由 zhuzhao 网关统一鉴权后内网调用（**用户侧零权限 + 服务间 AK/SK 验签**——2026-09-03 基线修订，基线 SSOT = zhuzhao `docs/phase3/16-external-integration.md` §9）。

## 当前形态（2026-09-03 职责收敛 + 需求澄清）

| 项 | 定稿 |
|---|---|
| 职责 | 类型注册 / Schema 演进 / 动态字段校验 / 数据 CRUD / 存储（乐观锁、软删除保留） |
| 事件 / 审计 | **移交 zhuzhao**（事件 = zhuzhao Asynq 业务操作点显式发布；审计 = zhuzhao 侧记录）；activelist 不感知事件、不写历史快照 |
| 进程 | 单一 apiserver 二进制（原 3 进程 → 1）；**无状态，目标多实例部署**（全部状态在 PG：乐观锁/软删/序列，无本地状态、无后台任务，天然可水平扩展） |
| 查询 | 仅唯一 id 分页 + 创建时间倒序（无过滤 / 排序 / 聚合） |
| 量级 | 百万行以内 |
| 存储 | **PostgreSQL**：每类型一张表 + `data` JSONB（id 自增 / 乐观锁 / 软删；建类型时建表，字段演进零 DDL） |
| Schema | **方案 D（单一当前版本）**：数据行不带 schema_version；兼容演进零迁移，破坏性变更懒执行（旧数据下次更新 422 + 迁移） |
| 导入导出 | JSON；导入幂等 = **全量替换**（事务内清表重灌、保留源 id；非 upsert、无需业务唯一键）；导出含 id / created_at；并发写由乐观锁保护（跨导入读改写 409，不静默写坏） |
| 数据性质 | **敏感高危数据 → 可靠性**；存储加密**已拍板不做**（2026-09-03，内网双 network + 审计一期不落字段值覆盖当前风险；跨网/合规变化再启）；日志脱敏**暂不做**（`sensitive` 标记 + 统一出口两钩子预留） |

## 文档

- [activelist.md](./docs/activelist.md) —— 原始完整设计方案（2455 行，正文为历史方案）+ 头部**收敛声明 · 最终画像 · 设计定稿补充（设计 SSOT）**
- [implementation-plan.md](./docs/implementation-plan.md) —— **实现计划（M-A）：目标/非目标、验收标准、里程碑、API 修订清单、代码目录、配置（实现 SSOT）**
- [activelist-review.md](./docs/activelist-review.md) —— 方案评审（**历史**：针对收敛前方案；数据模型侧发现仍可参考）
- [ADR-003-integration-contract.md](./docs/ADR-003-integration-contract.md) —— 与 zhuzhao 集成契约（**SSOT，2026-09-03 起以本项目为准**；含对 zhuzhao 能力需求汇总；zhuzhao 侧 `docs/adr/ADR-003` 为镜像）

## 与 zhuzhao 的关系

- **部署**：独立服务，内网双 network 隔离（`activelist_internal` + `zhuzhao_to_activelist`），仅 zhuzhao 容器可达
- **调用**：zhuzhao 网关统一 JWT / Casbin / Restrict 鉴权，透传 `X-Operator`（操作者）、`X-Request-ID`（链路追踪）
- **日志**：activelist 只记技术 / 运行日志（请求级 + 错误级，含 `X-Request-ID`；脱敏暂不做——钩子预留）；业务 / 审计日志由 zhuzhao 记录

## 状态

- 文档就绪：设计收敛定稿 + **实现计划就绪**（[implementation-plan.md](./docs/implementation-plan.md)，M-A 验收标准见其 §2）
- 代码未开始（首个里程碑 M-A1：项目骨架）
- 启动前置 ✅ **已就绪（2026-09-03）**：zhuzhao-utils v0.1.0 已发布并 pin（activelist 硬依赖 `logger` + `postgres`；见 ADR-003 D1 验证记录）——**M-A1 可立即开工**
- 排期归属：zhuzhao Phase 3 主线 **M-A（activelist 独立实现）**，与其他里程碑无链式依赖（2026-09-02 design-decisions §23.2，详见 ADR-003「排期与集成拆分同步」节）
