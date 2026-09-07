# ADR-003: activelist 集成形态 —— 独立服务 + 统一 PG + 事件总线对接（非事务耦合）

## 日期
2026-08-25

## 状态
已采纳（**2026-09-03 部分条款经职责收敛修订，见「2026-09-03 职责收敛修订」节**）。**2026-09-03 起本文件为集成契约 SSOT**（以 activelist 仓库为准，zhuzhao 侧 `docs/adr/ADR-003-activelist-integration-form.md` 降为镜像，反向同步债见文末「待同步清单」）。

## 背景
`docs/activelist.md` 设计了一个基于 MongoDB + Go 的动态多类型数据全生命周期管理平台（高可靠/高可用要求）。需要决策：它与 zhuzhao 的关系（独立 vs 内化）、数据库选型（Mongo vs PG）、以及是否需要跨事务一致。

经两轮验证澄清了真实业务场景：
- **场景1（事件广播）**：activelist 数据变更（如封禁 IP 列表新增一个 IP）→ 立马触发一个**预定义事件**（可被工单或其他模块订阅触发）。这是数据变更→发事件→订阅方响应的**事件驱动**模型，activelist 设计自身即"最终一致 / At-Least-Once + 幂等"，不要求与订阅方同事务。
- **场景2（多源摄取）**：工单数据源多样，除手动创建外，还有一部分数据由其他模块获取/产生。工单持有对各源的引用/快照，各源独立写、工单独立建，属**松一致**的 ingress 适配问题，不是与某数据源锁同一事务。

这两场景都不需要 activelist 与 zhuzhao 之间的跨事务一致。

## 决策
- **集成形态：activelist 作为独立服务**（不内化进 zhuzhao 单体，不做 C3 进程内合并）。保留部署边界 = 保留故障隔离；其高 SLA 负担（watcher HA、oplog/Resume Token 续传、节假日预案）不抬高 zhuzhao 单体关键性。
- **数据库：Mongo 迁移到 PostgreSQL（统一技术栈）**。理由见下"PG 优于 Mongo 的额外收益"。
- **耦合方式：事件总线对接，非事务耦合**。activelist 的变更事件**经 zhuzhao 网关 HTTP 事件摄入端点（带 `X-Operator` 鉴权）写入 zhuzhao 的 L1 `ticket_events`（事件事实源，ADR-001 既定）**，由 zhuzhao 侧消费者/Asynq worker 处理分发、**工单模块及其他模块订阅**；不要求跨服务/跨库事务，**事件表的数据库所有权始终在 zhuzhao 侧（activelist 不直接写 zhuzhao 库，符合 C2' 隔离）**。此处"事件总线"指 zhuzhao 的 **L1 事件源 + Asynq 执行器**组合，与 ADR-001/ADR-002 完全一致：**L1 是事件源（持久化/可重放），Asynq 是异步任务执行器，Asynq 不当事件总线**。
- **鉴权边界不变**：zhuzhao 网关统一 JWT/Casbin/Restrict 鉴权，内网信任透传 `X-Operator`；activelist 自身不做权限检查（见 `docs/activelist.md` §19.2）。
- **网络隔离不变**：apiserver 跨 `activelist_internal` + `zhuzhao_to_activelist` 双 network，仅 zhuzhao 容器可达（§18.2）。

### PG 优于 Mongo 的额外收益（转 PG 的加分项）
除"少运维一套数据库、与 zhuzhao 技术栈同源（JSONB/ltree/Outbox/Casbin-pgx）"外，转 PG 还有一项被忽视的硬优势：
- **事务能力 PG 远强于 Mongo**。PG 提供成熟的 MVCC + 多语句事务 + 保存点 + 外键级联 + 一致性快照读；Mongo 的"多文档事务"是 4.0 后才加、需副本集、有 16MB/60s 限制、且默认仍是单文档原子。尽管当前两个场景不要求跨服务事务，但 activelist 落到 PG 后，**其内部**的"写主数据 + 写历史快照 + 落事件"可用 PG 单机事务保证原子（Mongo 下只能靠异步队列最终一致）。即：统一 PG 不仅统一栈，还让 activelist 自身的可靠性底座更稳。

### Mongo vs PG 能力对比（设计阶段验证，不计成本）
| 能力（activelist 需求） | Mongo | PG | 结论 |
|------|------|------|------|
| 动态类型/字段（无重启扩展） | 文档模型天然 | JSONB / 运行时 CREATE TABLE | 持平 |
| 物理隔离（每类型独立集合） | 天然 | 分区表 / 每类型表 | Mongo 更自然 |
| 动态 JSON Schema 校验 | 应用层 | 应用层 / CHECK | 持平 |
| 事件捕获（断点续传） | Change Stream 开箱 | 逻辑复制 / 复用 zhuzhao Outbox | Mongo 开箱；PG 复用现有范式可行 |
| 乐观锁 | version 字段 | `UPDATE..WHERE version=` | 持平 |
| 历史快照审计 | `_history` 集合 | `_history` 表+分区 | 持平 |
| 高可用 | 副本集 | 流复制/PG Cluster | 持平 |
| **事务能力（内部原子）** | 多文档事务受限 | **完整 ACID 事务** | **PG 显著更优** |

## 理由
- 两真实场景均为事件驱动 / 松一致，跨事务一致无需求，内化（C3）不成立。
- 独立服务保留故障隔离，符合"单体保持简单、无微服务拆分需求"的总体决策（activelist 是外部引入能力模块，非拆分 zhuzhao 内部）。
- 转 PG 统一技术栈、复用 zhuzhao 已有 JSONB/ltree/Outbox/Asynq 积木，且 PG 事务能力更强，提升 activelist 自身可靠性底座。
- 事件总线对接契合 zhuzhao 已设计的 L1/L2 事件机制（ADR-001/ADR-002）：activelist 变更事件经 zhuzhao 网关事件摄入端点写入 L1 `ticket_events`（事件源），L1 消费者/Asynq worker 负责执行分发（Asynq 仅执行器，不当总线）。**activelist 与 zhuzhao 各自运行独立的 Asynq + Redis 实例，不共享任务执行器后端**（避免跨服务基础设施耦合）；事件事实始终以 zhuzhao 的 L1 表为源、不依赖 Redis 持久化（与 ADR-001 红线一致）。

## 后果
- 正面：技术栈统一（去 Mongo）、故障隔离保留、事件机制复用、PG 事务底座更稳、activelist 事件可被工单/其他模块统一订阅。
- 负面/风险：activelist 事件捕获从 Change Stream 改为 Outbox/逻辑复制，需改造（但复用 zhuzhao Outbox 范式增量可控）；独立服务仍需 E13 反向代理模块 + `SetForwardHeaders` 中间件。
- 待办（见下"待办"）：① 事件桥接缺口——activelist 变更事件如何汇入 zhuzhao 统一事件目录；② 工单多源 ingress——zhuzhao 工单模块需设计多种数据源适配器（activelist 仅其一）。

## 审计日志分工（已确认，两层）
- **不能只在 zhuzhao 记录 activelist 调用日志**，采用 `docs/activelist.md` §19.7 的两层审计：
  - **网关层（zhuzhao accesslog）**：记录谁/何时/访问了什么 API（method/path/actor/status/cost），proxy 路由**跳过 body**（防动态字段明文泄露到 zhuzhao 日志库）。
  - **业务层（activelist accesslog）**：记录谁对什么数据做了什么（含请求体 + 变更前后快照），由 activelist 按 Schema 标记**自行精确脱敏**（zhuzhao 不认识动态字段语义，黑名单命中不了手机号/身份证/薪酬）。
- 日志**基础设施复用 zhuzhao 的 `pkg/log/zap/logger`、`pkg/trace`、accesslog 核心**（C2' 同仓库直接 import/拷贝），但两进程写**各自日志目的地**（zhuzhao 审计库 / activelist 自身 log 集合）。

## 2026-09-03 职责收敛修订（覆盖本节上文部分条款）

> 上文为 2026-08-25 原始决策。2026-09-03 经「activelist 需求检查 + 业界对标」讨论（结论：动态数据模型平台是成熟类别，业界有 NocoBase/Teable/Twenty/NocoDB 等大量同类，但均连带事件/审计/UI/组织集成，复用=引入整套独立系统），所有者拍板**职责收敛**，覆盖上文以下条款。

### 收敛后 activelist 定位
- **activelist = 动态数据模型平台**（唯一职责）：类型注册 / Schema 演进 / 动态字段校验 / 数据 CRUD / 存储（乐观锁、软删除保留）。
- **事件驱动移交 zhuzhao**：activelist 数据变更 → 事件由 **zhuzhao Asynq** 承担（zhuzhao 在业务操作点显式发布，L1 事件源不变，ADR-001/002 红线不变）；activelist **不再实现 Change Stream 事件捕获**（原 `docs/activelist.md` §7/§8 watcher 高可用、Resume Token、Redis fallback 全部移除）。进程 **3→1**（仅 apiserver）。
- **审计（历史快照）移交 zhuzhao**：activelist 不写历史快照、不记业务语义日志；审计由 zhuzhao 侧记录（✅ **落点机制已拍板（2026-09-03）**：见下方「审计落点机制」专节——zhuzhao client 封装层同步写 `activelist_audit_log` + 本地重投队列；activelist 义务 = 写接口返回变更后完整文档）。
- **独立部署保留**：独立服务 + 独立库 + 独立数据库（故障隔离不变）；**zhuzhao 作对外网关**（网关尚未实现）调用 activelist。
- **服务级通信鉴权（2026-09-03 基线修订，覆盖「零认证」原口径）**：activelist 对来自 zhuzhao 的调用**验 AK/SK HMAC 签名**（utils `aksk`，按调用方发 SK；明文 `X-Operator` 入签名覆盖——不可伪造；专用 network 保留为第二道防线）。用户侧仍零权限（不判定）。基线 SSOT = zhuzhao `docs/phase3/16-external-integration.md` §9。

### 覆盖上文条款对照
| 上文条款 | 收敛后 |
|---|---|
| 「审计日志分工（两层）」：网关层跳过 body + **activelist 业务层自脱敏 accesslog**（§52–56） | **修订**：审计/业务日志归 **zhuzhao**；activelist 只记**技术/运行日志**（请求级 + 错误级，不记业务语义；脱敏暂不做——钩子预留）；`X-Request-ID` 贯穿两层关联排查 |
| 待办 **G4 两层审计** | **修订并关闭**：zhuzhao 侧审计记录（✅ 落点机制已拍板 2026-09-03，见「审计落点机制」专节：client 封装层 + `activelist_audit_log`） |
| 待办 **G1/G2**（Change Stream→Outbox/逻辑复制改造；activelist 变更事件桥接汇入 L1） | **简化**：activelist 侧不再有事件捕获职责；事件 = zhuzhao 调 activelist 成功后**业务操作点显式发布**（G2 含义从「activelist 变更事件汇入」改为「zhuzhao 调用后发布事件」） |
| 建议阶段「Phase 3 启动后（L1+Asynq 就绪后）」 | 不变（L1/Asynq 就绪后，事件侧已由 zhuzhao 承担） |
| 转 PG 收益「写主数据 + 写历史快照 + 落事件 可用 PG 事务原子」 | **减弱**：历史快照/事件外置后，activelist 内部只剩主数据写，事务需求大幅简化 |

### 日志与共享 utils（2026-09-03 新增拍板）
- **共享 utils 项目**：`zhuzhao/internal/pkg/` 通用代码抽取为**独立共享项目**（✅ 2026-09-03 定名 `zhuzhao-utils` 并发布 v0.1.0），**zhuzhao 与 activelist 均引用**。
- **抽取范围（2026-09-03 已核实依赖面）**：
  - 零内部依赖可直接抽：`crypto` / `errcode` / `jsonutil` / `resource` / `validate`；`response` 依赖 `errcode`，随包抽取。
  - 需 **config 解耦**后抽：`jwt` / `logger` / `postgres` / `redis`（当前依赖 `zhuzhao/internal/config` 的 LogConfig/DBConfig 等，抽取时结构体参数化或随包自带）。
  - `resource` ✅ 已复核（2026-09-03）：绑定 zhuzhao 权限领域，**留 zhuzhao 不抽**（design-decisions §25.3）。
- **关键约束**：新项目必须**移出 `internal/` 目录**（否则仍受 Go internal 约束，无法被独立 module 引用）；zhuzhao 全仓库 import 改新 module path；**一次性完成避免双份维护**；完成后跑全量门禁（`make lint` / `make test-unit` / `make test-integration` / `make acceptance`）。
- **触发时机** ✅ 已完成（2026-09-03，zhuzhao-utils v0.1.0 发布并 pin；M-A 前置解除）。
- **业界对标结论（为何自研薄层）**：收敛后 activelist 为薄层动态数据模型平台；同类开源（NocoBase / Teable / Twenty / NocoDB / Baserow 等）均连带事件/审计/UI/组织集成，复用=引入整套独立系统（多为 Node/TS 栈、独立运维）；自研薄层 + 共享 utils 复用 zhuzhao 积木（PG/JSONB/Asynq/errcode）更符合技术栈统一与运维最小化。

### 需求澄清 · 最终画像与存储引擎（2026-09-03 追加）

经需求逐条澄清，收敛后真实需求画像（完整表见 `docs/activelist.md` 收敛声明·最终画像）：任意自定义类型、字段= `int`/`string`/二者列表（无嵌套/关系）；activelist 用户侧零权限 + 服务间 AK/SK 验签（2026-09-03 基线修订，原「零认证」口径已覆盖）；查询=仅 id 分页 + 时间倒序；量级百万行内；**敏感高危数据 → 可靠**（存储加密已拍板不做、日志脱敏暂不做——均 2026-09-03 定稿，原文「暂不需要 ⚠️ / 仍需」已过时）；低频 Schema 演进；软删保留；导入导出 JSON + 幂等 + 并发；id 自增。

**存储引擎评审（PG 仍为最优，收敛后更无悬念）**：

| 维度 | PG | Mongo | 收敛后判断 |
|---|---|---|---|
| 技术栈统一 / 少运维一套 | ✅ zhuzhao 已用 PG | ❌ 另运维一套 | **PG 决定性优势** |
| 动态字段 | JSONB 等效 | 文档天然 | 持平 |
| 事件捕获（Change Stream） | — | ✅ 原生 | **已外置，Mongo 最大优势失效** |
| id 自增 | ✅ SERIAL/IDENTITY | ❌ 自实现计数器 | **PG 优势** |
| 事务 / 原子（乐观锁、幂等、软删） | ✅ 强 | 弱 | **PG 优势** |
| 百万行 + id 分页 | ✅ | ✅ | 持平 |
| 运维 / 监控 / 备份 | zhuzhao 已有经验 | 新引入 | **PG 优势** |

**存储模型（PG 落地形态）**：**每类型一张表 + `data` JSONB 存动态字段**——
```
col_<type>(
  id            BIGSERIAL PRIMARY KEY,  -- 自增 id
  data          JSONB,                  -- 动态字段（int/string/列表）
  version       INT,                    -- 乐观锁
  status        TEXT,                   -- active / deleted（软删保留）
  created_at / updated_at TIMESTAMPTZ,
  created_by / updated_by TEXT          -- 来自 X-Operator
)
```
建类型时 CREATE TABLE（低频）；字段演进只改 schema 定义 + 应用层校验，**零 DDL**（字段全在 JSONB）。原 Mongo 方案 `col_<type>` 独立集合语义等价迁移。
>
> **2026-09-03 定稿补充（覆盖上文 sketch 注释）**：① Schema 采用**方案 D**——单一当前版本，数据行**不带 `schema_version`**（§5.4 方案 B 与 §23 F4 悬案就此关闭，F4 采纳；破坏性变更允许提交、懒执行，旧数据下次更新 422 提示迁移）；② 导入幂等 = **全量替换**（单事务清表——含软删行——按文件插入、保留源 id、version 重置 1、setval 序列；非 upsert，**不需要每类型业务唯一键**；导出格式必须含 id / created_at）。详见 activelist 仓库 `docs/activelist.md` 头部「2026-09-03 设计定稿补充」。（zhuzhao 侧镜像待同步此两项，见文末「待同步清单」。）

## 排期与集成拆分同步（2026-09-02/03 拍板，源自 zhuzhao design-decisions §22.3/§23.2、phase3/13 M-A）

- **里程碑重排**：zhuzhao Phase 3 现行主链 = M0 启动准备 → M-E（事件与任务平台）→ **M-A（activelist 独立实现）** → M-HR（HR 同步）→ M-Mig（迁移准备）；原「M4 activelist 集成」被 M-A 取代。**M-A 与其他里程碑无链式依赖**（修订下文「建议阶段：Phase 3 启动后（L1+Asynq 就绪后）」的排期表述）；M-A 前置 = **共享 utils 抽取（🚦）**；验收 = 按 activelist 项目自身验收；人日估算约 1.5–3（⚠️ 待校准）。
- **集成拆两半（§22.3，部分被 §23.2 修订）**：E13 反代 / G1 Mongo→PG / G3 多源 ingress **降为架构蓝图保留（🚦）**，由 activelist 独立项目成型触发；**activelist 使用独立数据库（与工单库故障隔离）拍板认可**。
- **工单非首数据源（§23.2）**：**外部事件接入契约由 activelist 侧定义**，取代 §22.3 的 ActivelistWriter 方向；工单 Phase 2 封版、自研暂缓，转为公司内部工单平台的对接方（迁移后集成项 🚦）。
- **zhuzhao 侧事件/审计执行形态（语境参考）**：zhuzhao 事件基建 = M-E taskrunner（独立仓库 + 独立部署 + 独立 Redis；预置动作 = 回调 zhuzhao 内网端点执行，业务 handler 在 zhuzhao；业务审计落 zhuzhao `audit_logs`）——「事件与审计移交 zhuzhao」的接收方即此形态。

## activelist 对 zhuzhao 的能力需求（汇总，2026-09-03 整理）

| # | 能力需求 | zhuzhao 侧载体 | 状态 | 对 activelist 的阻塞关系 |
|---|---------|---------------|------|------------------------|
| D1 | 共享 utils：`logger` / `postgres`（硬依赖），`errcode` / `response` / `jsonutil` / `validate` / `crypto`（按需） | zhuzhao-utils 独立项目 | ✅ **迁移完成，已验证（2026-09-03）** | **已解除——M-A1 可开工** |
| D2 | 反向代理 + header 透传（E13：`app/service/proxy/` + `SetForwardHeaders` + Restrict 资源 `activelist` + accesslog 跳过 body） | zhuzhao E13 | 蓝图 🚦（未开始） | **不阻塞开发；阻塞联调与上线**（activelist 用户侧零权限，无网关不能对外暴露） |
| D3 | 业务审计记录 | zhuzhao client 封装层 + `activelist_audit_log` 表 | ✅ **已拍板**（2026-09-03，机制见下方专节：双侧记录 + X-Request-ID 关联；脱敏暂不做＝风险接受） | 已解除阻塞（zhuzhao 侧实现项：client 层 + 审计表；activelist 侧义务已定稿） |
| D4 | 事件发布（zhuzhao 业务操作点显式发布；工单非首数据源，接入契约由 activelist 侧定义） | zhuzhao M-E taskrunner（平台已就绪：M1/M2 完成 2026-09-03，见 taskrunner 仓库） | ⏳ 平台就绪；activelist 事件接入待其成型 | **无依赖**（activelist 不感知事件） |
| D5 | 网络隔离（双 network，仅 zhuzhao 容器可达 apiserver 8080） | 双方部署约定 | activelist 自理 docker-compose | 部署期事项（M-A6） |

**D1 验证记录（2026-09-03，activelist 侧核对）**：

- ✅ 10 包齐备（aksk / crypto / errcode / jsonutil / jwt / logger / postgres / redis / response / validate），module = `github.com/tracerbiubiubiu/zhuzhao-utils`，已推送远端。
- ✅ `logger`（`logger.New(logger.Config)` → `*slog.Logger`，自带 Config，无 zhuzhao config 耦合）、`postgres`（`postgres.New(postgres.Config)` → `*pgxpool.Pool` + cleanup，`ApplyDefaults`/`DSN`/statement_timeout/application_name 齐备）——activelist 可直接使用。
- ✅ `errcode`/`response` API 满足统一响应包装（`OK/OKPage/Fail/Error/Conflict/...`；activelist 的 `detail.error_code` 字段自行适配）。
- ✅ zhuzhao 主仓已切换 import；`internal/pkg/errcode` 为**转发 shim**（业务码 20000 起留 zhuzhao、框架码进 utils、类型别名统一——有意分层，非双份维护）；`internal/pkg/resource` 留 zhuzhao（权限域绑定，activelist 无用户权限判定不需要）。另：utils 已含 `aksk` 包（2026-09-03 AK/SK 基线修订新增，M-A6 验签中间件依赖已就绪）。
- ⚠️ 遗留（不阻塞，utils 侧可选补充）：① `logger` / `postgres` 无单测（其余包有）；② `postgres.Config` 可加 `LockTimeout` 字段（activelist 导入期间的常规写快速失败需要；不加则 activelist 以会话级 `SET lock_timeout` 自理）。

## 审计落点机制（已拍板 2026-09-03：双侧记录 + request_id 关联；脱敏暂不做）

**形态**：zhuzhao 记「谁、何时、干了什么、调了什么接口、返回值是什么」＝**审计正本**（落库表）；activelist 记「何时被调用、参数是什么、结果是什么」＝**访问日志**（技术日志）；同一 `X-Request-ID` 贯穿关联。

| | zhuzhao（审计正本） | activelist（访问日志） |
|---|---|---|
| 落点 | **统一 activelist client 封装层**同步写 `activelist_audit_log` 表 | 技术日志（slog 文件，轮转，M-A6） |
| 记录 | operator、时间、action（snake_case）、method + path（含 type/id）、HTTP 状态、**返回值原样**（JSONB + 截断上限 16KB）、trace_id | 时间、operator（X-Operator）、method + path、**请求参数原样**（4KB 截断）、结果状态、trace_id（X-Request-ID） |
| 持久性 | 审计表按月分区，保留期独立；不可丢的正本 | 日志轮转可过期，排障辅助（保留建议 30–90 天，兼作审计缺行时的事后线索） |
| 可靠性 | client 层调用成功后**同请求路径同步写**；失败落本地重投队列 + 告警 | — |

**要点**：

1. **记录点 = client 封装层**（反代与内部直调的公共必经点；网关 accesslog 会漏内部调用）。client 层同时负责 `X-Request-ID` 的**生成与透传**——zhuzhao 内部直调没有网关生成 request id，必须由 client 层补上，否则关联断链。
2. **审计表**：不复用工单域 `ticket_events`；索引 `(operator, created_at)` / `(type_name, data_id, created_at)` / `(trace_id)`；**导入/导出按批次一行**，response 存批次汇总（行数/耗时/max_id），**不存全量数据**。
3. **脱敏暂不做（明确风险接受）**：敏感值将明文存在于 activelist 日志与 zhuzhao 审计表。缓解＝暴露面收敛：内网双 network 不变；**E13 网关 accesslog 仍跳过 body**（敏感值在 zhuzhao 只落审计表一处，不进 accesslog 第二份）；日志文件权限收紧；保留期收敛。**预留两个零成本钩子**：schema 字段定义格式预留 `sensitive: true` 标记（暂不实现逻辑）；activelist 日志输出走统一中间件出口（将来开脱敏只改一处）。**重估触发**：合规要求 / 跨网部署 / 安全事件。
4. **水位对账暂缓（风险接受）**：重投队列也丢失时审计缺行（窗口 = activelist 已提交 + 审计未落）；activelist 访问日志可作事后人工回填线索；数据行 version 单调递增，将来补自动对账成本低。
5. **不产生审计**：乐观锁 409、校验 422（操作未生效）；单条 GET 不记（噪声）；**export 记批次行**（批量拉敏感数据属访问审计）。
6. **已否选项**：activelist 自写审计表（违背收敛拍板）；反代中间件记审计（漏内部调用）；事件驱动审计（事件职责已移交 zhuzhao 业务操作点）。

**activelist 侧契约**（已定稿于 implementation-plan §4）：写接口响应返回变更后完整文档（id / version / data / updated_at）；按 id 可查；导入响应返回批次汇总。审计视图（脱敏版响应）待脱敏启用时再增补，additive 不破坏契约。

## 待办

> 现行效力注（2026-09-02/03）：**E13 / G1 / G3 为架构蓝图保留 🚦**（§22.3/§23.2，由 activelist 独立项目成型触发）；**G4 已关闭**（审计归 zhuzhao，落点机制已拍板——见「审计落点机制」专节）；G2 事件桥接由「zhuzhao 业务操作点显式发布」取代。

- **E13（zhuzhao 侧）**：反向代理模块 `app/service/proxy/` + `SetForwardHeaders` 中间件 + Restrict 资源 `activelist` + accesslog 对 `/api/v1/data/*` 跳过 body（仅记 HTTP 元信息）。
- **G1（activelist 侧，转 PG）**：Mongo → PG 迁移设计（动态集合→分区表/每类型表；Change Stream→Outbox/逻辑复制；历史快照落 PG 表）。**含日志 writer 迁移**：§19.7.1 的 `mongo_writer.go` / `NewMongoWriteSyncer` 需改为 PG writer，否则转 PG 后日志仍依赖 Mongo。
- **G2（集成缺口）**：~~activelist 变更事件 → zhuzhao 统一事件目录（L1 事件源）的桥接设计~~（已被收敛修订取代：activelist 无事件职责；事件 = zhuzhao **业务操作点显式发布**——client 封装层对 activelist 写操作成功后发布，非「网关事件摄入端点」路径）。
- **G3（zhuzhao 工单侧）**：多源 ingress 适配器设计（手动 + activelist + 其他模块）。
- **G4（审计层）**：~~两层审计落地——zhuzhao 网关层 + activelist 业务层自脱敏 accesslog~~（已被收敛修订取代：审计归 zhuzhao client 封装层 + `activelist_audit_log`，✅ 见「审计落点机制」专节；脱敏暂不做）。

## 建议阶段

> ⚠️ **2026-09-02 修订（design-decisions §23.2）**：现行排期 = Phase 3 主线 **M-A（activelist 独立实现）**，与其他里程碑无链式依赖；M-A 前置 = 共享 utils 抽取（🚦）。下文为 2026-08-25 原表述，保留作历史。

- **Phase 3 启动后**（L1 事件机制 + Asynq 就绪后）。原拟 Phase 2b，因前置依赖 L1/Asynq 实际于 Phase 3 启动时落地，已决策顺延至 Phase 3 启动后——避免为 activelist 单独写一套临时事件分发再迁移回 L1（违反 ADR-001「不偷工减料」原则）。**HR 目录同步不依赖 L1，仍属 Phase 2b**（desired-state reconciliation 直接写表，非事件消费者）。前置依赖：L1 事件机制 + Asynq 执行器就绪（Phase 3 启动时实现，见 [ADR-001](./ADR-001-event-mechanism-l1-steady-state.md)/[ADR-002](./ADR-002-asynq-async-task-executor.md)）、网关反代模块（E13）。
- Mongo→PG 迁移（G1）与方案 F 可并行设计，不阻塞 zhuzhao 主链路；若 2b 排期紧张，标为 Phase 2 按需增强。

## 关联文档
- 本仓库 `docs/activelist.md` §3（设计原则）、§7.2（Change Stream）、§18（部署）、§19（与 zhuzhao 集成）——**2026-09-03 迁移至独立仓库**（`github.com/tracerbiubiubiu/activelist`），SSOT 以 activelist 仓库为准
- zhuzhao 仓库 `docs/adr/ADR-001-event-mechanism-l1-steady-state.md`（L1 事件源长期稳态）
- zhuzhao 仓库 `docs/adr/ADR-002-asynq-async-task-executor.md`（Asynq 作为异步任务执行器；注意 Asynq 不当事件总线，事件源仍是 L1）
- zhuzhao 仓库 `docs/roadmap.md`（外部能力集成：activelist 小节；以上三份均不在本仓库）

## 待同步清单（activelist → zhuzhao 镜像，反向同步债）

本文件升格 SSOT 后，以下 activelist 侧定稿需反向同步到 zhuzhao 侧镜像。**2026-09-04 核验：均已同步/闭环**（zhuzhao 侧 ADR-003 镜像已按 SSOT 更新、roadmap 已修正「零认证」/「aksk 新增规划」表述、D4 载体状态已更新）。后续 activelist 侧定稿变更时按本清单反向同步。

1. ~~方案 D 定稿~~ ✅ 已同步：zhuzhao ADR-003 镜像 §116、roadmap、phase3-13 M-A 行均已更新（数据行不带 `schema_version`、方案 D 采纳）。
2. ~~导入幂等 = 全量替换~~ ✅ 已同步：zhuzhao 侧 ADR-003 / roadmap / phase3-13 M-A 行均已改为「全量替换、保留源 id、不需要每类型业务唯一键」。
3. ~~roadmap.md activelist 小节滞后~~ ✅ 已更新：审计归 zhuzhao、事件为 zhuzhao 业务操作点显式发布、M-A 无链式依赖均已修正。
4. ~~zhuzhao 侧 ADR-003 工作区改动未提交~~ ✅ 已提交。

## 变更记录

| 日期 | 变更 |
|---|---|
| 2026-09-03 | 建档（自 zhuzhao 镜像升格 SSOT）；同日职责收敛修订 + 需求澄清 + 存储定稿 |
| 2026-09-03 | **AK/SK 基线修订**：服务间通信统一 AK/SK HMAC 签名（utils `aksk`），activelist 由「零认证」改为**验签**（M-A6 接线；X-Operator 入签名覆盖）；用户侧零权限不变；专用 network 降为第二道防线。基线 SSOT = zhuzhao 16 号 §9 |
