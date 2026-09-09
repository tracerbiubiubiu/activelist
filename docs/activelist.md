# 基于 MongoDB + Go 的动态多类型数据全生命周期管理平台技术方案

---

> **⚠️ 2026-09-03 职责收敛声明（SSOT = 本文件头部定稿 + 本仓库 `docs/ADR-003-integration-contract.md`；zhuzhao 侧 ADR-003 / design-decisions 为镜像，2026-09-03 起以 activelist 仓库为准）**：本方案为原始完整设计（事件驱动 + 全生命周期审计 + 三进程高可用）。经职责收敛拍板，**activelist 收窄为「动态数据模型平台」**——只负责：类型注册 / Schema 演进 / 动态字段校验 / 数据 CRUD / 存储（乐观锁、软删除保留）。**事件驱动与审计（历史快照）移交给 zhuzhao**（事件 = zhuzhao Asynq；审计 = zhuzhao 侧记录），activelist 不感知事件、不写历史快照。**独立部署保留**（独立服务 + 独立库 + 独立数据库，zhuzhao 作对外网关调用）。进程由 3 个减为 1 个（仅 apiserver）。
>
> 本文后续正文仍为完整历史方案，章节有效性如下：
> - **继续有效（数据模型层）**：§5.1/§5.2（数据/元数据模型）、§6.1–6.5（Registry/Repository/Validation/Schema 演进/软删除状态机）、§10（并发控制）、§15（安全——其中 §15.1 认证口径已被 AK/SK 基线修订覆盖，见下）。
> - **已被取代（仅作历史参考）**：§6.7（查询安全——按最终画像收窄为 id 分页 + 时间倒序）、§6.9（API 清单——以 [`implementation-plan.md`](./implementation-plan.md) §4 为准）；§6.8（错误码）语义仍沿用。**实现细节与本文件冲突时，一律以 implementation-plan.md 为准。**
- **§15.4 仅「缺失兜底 system」规则有效**：其溯源链路（Change Stream/历史集合）与「不感知字段敏感性」表述已被收敛/AK-SK 修订取代（`sensitive: true` 钩子预留，见 implementation-plan §7）。
- **§4（技术栈）已废**：MongoDB/Redis/Asynq/asynqmon 行全部失效（收敛后 = gin + PG + zhuzhao-utils）；现行技术栈以 implementation-plan.md §5/§6 为准。
- **§6.4 中 schemaVersion 演进流程已被方案 D 取代**（单一当前版本，见头部定稿补充）；仅并发演进 409 语义仍被引用（implementation-plan §7）。
> - **移交 zhuzhao（不再由 activelist 实现）**：§5.3 历史集合（审计）、§7 事件驱动架构（Change Stream + Asynq worker）、§8 事件侧高可用（watcher HA / Redis fallback）、§12 可靠性矩阵（事件部分）、§13 流程四/五/六。
> - **需复核调整**：§6.6（数据迁移）、§10.4（跨集合事务——事件/历史剥离后内部事务需求简化）、§14（注意事项）、§18/§19（部署/集成按 ADR-003 修订）。
> - **日志**：activelist 只记技术/运行日志（请求级 + 错误级，含 `X-Request-ID`，不记业务语义；脱敏暂不做，钩子预留），业务/审计日志由 zhuzhao 记；日志代码复用 = 从 zhuzhao `internal/pkg` 抽取的**共享 utils 项目**（见 ADR-003 修订）。
> - **业界对标结论（2026-09-03）**：activelist 收敛后 = 薄层动态数据模型平台；同类开源（NocoBase / Teable / Twenty 等）均连带事件/审计/UI/组织集成，复用需引入整套独立系统；自研薄层 + 复用共享 utils 更划算（详见 ADR-003 修订节）。
>
> ### 2026-09-03 需求澄清 · 最终画像（SSOT = ADR-003 修订节）
>
> 经需求逐条澄清，收敛后真实需求画像如下（**远轻于本文原始设计**）：
> | 项 | 定稿 |
> |---|---|
> | 类型 | 任意自定义类型；字段类型 = `int` / `string` / 二者的**列表**（无对象/嵌套/关系/公式） |
> | 认证/鉴权 | **用户侧零权限**（不判定，zhuzhao 网关统一鉴权 + X-Operator，§19.2 不变）；**服务间验 AK/SK HMAC 签名**（2026-09-03 基线修订，utils `aksk`，X-Operator 入签名覆盖——不可伪造；专用 network 降为第二道防线） |
> | 查询 | **仅按唯一 id 分页 + 创建时间倒序**；无过滤、无排序参数、无聚合（§6.7 动态查询引擎整块砍掉） |
> | 量级 | 百万行以内 |
> | 可靠性 | 高——因存**敏感高危数据**（可靠性由安全驱动，非规模驱动）；存储加密**已拍板不做**（2026-09-03：内网 + 审计一期不落字段值覆盖当前风险；跨网/合规变化再启）；日志脱敏**暂不做**（2026-09-03 拍板，钩子预留——schema `sensitive` 标记 + 统一日志出口） |
> | Schema 演进 | **方案 D 定稿**：单一当前版本、数据行不带 schema_version；兼容变更零迁移；破坏性变更懒执行（旧数据下次更新 422 + 迁移），§6.6 重型迁移工具简化或按需 |
> | 软删除 | **保留**（高危数据误删可恢复 + zhuzhao 审计配合） |
> | 导入导出 | **JSON**；导入幂等 = **全量替换**（事务内清表重灌、保留源 id；非 upsert，无需业务唯一键）；导出含 id / created_at；并发写由乐观锁保护（跨导入读改写 409） |
> | 唯一 id | **自增**（BIGSERIAL） |
> | 存储引擎 | **PostgreSQL（评审结论：仍为最优）**；模型 = **每类型一张表 + `data` JSONB 存动态字段**（建类型时 CREATE TABLE，字段演进零 DDL；表结构见 ADR-003 修订节） |
>
> ### 2026-09-03 设计定稿补充（导入语义 + Schema 方案 D · SSOT）
>
> 经二次确认拍板，覆盖上文表中 upsert / 多版本相关表述：
> - **导入幂等 = 全量替换（非 upsert）**：单事务内 DELETE 该类型全表（**含软删行，替换不可恢复**）→ 按文件插入（**保留源 id**、`version` 重置 1、`created_at`/`updated_at`/`created_by`/`updated_by` **沿用文件值，空缺回退导入时刻/'system'**——2026-09-08 实现修订：保留源操作者溯源，重灌不重写历史；原表述「`updated_at`/`updated_by` = 导入时刻/操作者」被覆盖）→ `setval` 序列至 max(id)。重导同一文件结果一致即幂等；**不要求每类型声明业务唯一键**（原 upsert 方案作废）。导出格式必须含 `id` 与 `created_at`。
> - **与并发的关系**：常规 CRUD 乐观锁不变；跨导入的"拉数据 × 修改字段"因 version 重置必然 409 重拉，**不会静默写坏**（若保留文件中的旧 version 则会出现跨记录静默覆盖，故必须重置）；替换事务期间并发写阻塞至提交（≤ 百万行、导入低频，可接受）。
> - **Schema 采用方案 D（§23 F4 采纳，悬案关闭）**：单一当前版本，数据行**不带 schema_version**；写路径统一"读旧行 → 合并变更 → 对当前 schema 全量校验 → 乐观锁写入"，§5.4 分类校验算法与方案 A/B/C 讨论**整体废弃（正文作历史保留）**；破坏性变更**允许提交、懒执行**（演进时不拒绝，旧数据下次更新时 422 提示迁移——与 F4 原文"演进时拒绝 422"的偏差，已拍板取懒执行）；字段删除 = 标 deprecated，残留值留 JSONB 无害，物理清除随迁移。
> - 审计配合：导入按**批次**落一条审计（operator / type / 行数 / 时间），不逐行。

---

## 一、方案概述

本方案构建一个支持**用户自定义数据类型、动态字段扩展、高可靠事件驱动、全生命周期追溯**的后端系统，适用于数据类型持续增长、字段频繁变动、需要审计溯源和高可靠性事件处理的业务场景。

核心设计围绕 **"物理隔离、动态校验、事件驱动、全生命周期管理"** 四大原则展开。

### 1.1 部署定位

本服务**部署在内网**，不独立处理认证鉴权，由 **zhuzhao 网关统一鉴权**后通过 Docker 内网调用。activelist 只暴露内网端口，仅 zhuzhao 可达。具体集成方案详见第十九节。

### 1.2 进程组成

本服务由 3 个独立进程组成：

| 进程 | 职责 | 部署形态 |
|------|------|---------|
| **apiserver** | HTTP API 服务，处理类型注册、Schema 演进、数据 CRUD | 多副本无状态 |
| **watcher** | Change Stream 监听器，捕获数据变更并投递到 Asynq | 单副本（多副本选主待定） |
| **worker** | Asynq Worker，处理历史快照写入 + 业务异步任务 | 多副本无状态 |

辅助二进制：
- `cmd/migrate` — 数据迁移工具（按需手动执行）

---

## 二、整体架构图

```
┌──────────────────────────────────────────────────────────────────────┐
│                      外部客户端 / 前端                                 │
└──────────────────────────────┬───────────────────────────────────────┘
                               │ HTTPS
┌──────────────────────────────▼───────────────────────────────────────┐
│                   zhuzhao 网关（认证 + 鉴权）                          │
│           JWT 校验 + Casbin 接口级 + Restrict 资源级                   │
└──────────────────────────────┬───────────────────────────────────────┘
                               │ HTTP（内网，Docker network）
                               │ Header: X-Operator, X-Request-ID
┌──────────────────────────────▼───────────────────────────────────────┐
│                  activelist apiserver（多副本无状态）                  │
│  ┌───────────────┬───────────────┬───────────────┐                  │
│  │ 类型注册管理  │ CRUD 数据操作 │ Schema 校验   │                  │
│  │ (Registry)   │ (Repository)  │(gojsonschema) │                  │
│  └───────────────┴───────────────┴───────────────┘                  │
└──────────┬───────────────────────────────────────────┬───────────────┘
           │                                           │
           ▼                                           ▼
┌─────────────────────────────┐         ┌──────────────────────────────┐
│   MongoDB 3 节点副本集      │         │     Redis（初期单点）         │
│  ┌────────────┬───────────┐ │         │   （Asynq 任务队列后端）      │
│  │ 数据集合    │ 元数据    │ │         │   └────────────┬─────────────┘ │
│  │ col_user   │ schema_   │ │         │                │               │
│  │ col_product│ definitions│ │         │                │               │
│  │ col_order  │           │ │         │                │               │
│  │ 历史集合    │ resume_   │ │         │                │               │
│  │ col_*_hist │ tokens    │ │         │                │               │
│  │            │ event_    │ │         │                │               │
│  │            │ fallback  │ │         │                │               │
│  └────────────┴───────────┘ │         │                │               │
└──────────┬──────────────────┘         │                │               │
           │ Change Stream              │                │               │
           ▼                            │                │               │
┌─────────────────────────────┐         │                │               │
│  activelist watcher         │         │                │               │
│  （单副本 / Phase2 选主）    │ Enqueue│                │               │
│  - Resume Token 持久化      │────────▶│                │               │
│  - stream 错误重连          │         │                │               │
│  - fallback 表兜底          │         │                │               │
└─────────────────────────────┘         │                │               │
                                        │                │               │
                                        ▼                │               │
                              ┌──────────────────────┐  │               │
                              │  Asynq 任务队列       │  │               │
                              │  （持久化到 Redis）   │  │               │
                              └──────────┬───────────┘  │               │
                                         │              │               │
                                         ▼              │               │
                              ┌──────────────────────┐  │               │
                              │ activelist worker    │  │               │
                              │ （多副本无状态）       │  │               │
                              │ ┌──────────────────┐ │  │               │
                              │ │ 中间件 Handler   │ │  │               │
                              │ │  1. 历史 Handler │ │  │               │
                              │ │  2. 业务 Handler │ │  │               │
                              │ │     (按需扩展)    │ │  │               │
                              │ └──────────────────┘ │  │               │
                              └──────────────────────┘  │               │
                                        │                │               │
                                        ▼                │               │
                              ┌──────────────────────┐  │               │
                              │ asynqmon 监控面板    │  │               │
                              └──────────────────────┘  │               │
```

**关键存储集合**：

| 集合 | 用途 |
|------|------|
| `col_<typeName>` | 业务数据集合 |
| `col_<typeName>_history` | 历史快照（不可变审计日志） |
| `schema_definitions` | Schema 定义 + 版本管理 |
| `resume_tokens` | Watcher Resume Token 持久化 |
| `event_fallback` | Redis 故障时事件兜底存储 |

---

## 三、核心设计原则

| 原则 | 说明 |
|------|------|
| **物理隔离** | 每个数据类型拥有独立集合（`col_<typeName>`），查询无需 `docType` 过滤，索引更纯粹 |
| **无重启扩展** | 新增类型或字段无需修改 Go 代码、无需重新编译、无需重启服务 |
| **动态校验** | 用户定义的字段规则转为 JSON Schema，通过 `gojsonschema` 在应用层校验 |
| **读写分离** | 写入（Insert/Update）严格校验，读取（Query/Delete）零校验直接透传 |
| **事件驱动** | Change Streams + Asynq 实现可靠的事件捕获与异步处理解耦 |
| **全生命周期管理** | 数据历史表 + 版本化 Schema，实现数据和 Schema 的双重全生命周期追溯 |
| **最终一致** | 主数据写入即同步可见，历史快照异步落盘（At-Least-Once + 幂等） |
| **不做跨集合事务** | 主数据走单文档原子，历史走异步队列，明确不使用跨集合事务（详见 10.4） |

---

## 四、技术栈

| 层级 | 技术选型 | 作用 |
|------|---------|------|
| **数据库** | MongoDB 5.0+（3 节点副本集） | 存储动态文档 + 提供 Change Streams |
| **缓存/队列** | Redis（初期单点，生产可升级主从+哨兵） | Asynq 任务队列后端 |
| **Go 框架** | gin | HTTP 接口层 |
| **MongoDB 驱动** | go.mongodb.org/mongo-driver/v2 | 官方驱动，原生支持 Change Streams |
| **动态校验** | github.com/xeipuuv/gojsonschema | JSON Schema 运行时校验 |
| **任务队列** | github.com/hibiken/asynq | 可靠的任务分发 + 重试 + 监控 |
| **任务监控** | asynqmon（github.com/hibiken/asynqmon） | Asynq Web UI |
| **并发控制** | 乐观锁（version 字段） | 解决 Schema 修改和"读-改-写"冲突 |

---

## 五、数据模型设计

### 5.1 数据集合（存储具体数据）

- **命名规范**：`col_<typeName>`（例如 `col_user`、`col_product`）
- **存储方式**：使用 `bson.M`，所有动态字段作为文档顶级字段平铺

```
{
  "_id":           ObjectId("..."),
  "version":       1,              // 乐观锁版本号（系统字段）
  "schemaVersion": 2,              // 数据使用的 Schema 版本（系统字段）
  "status":        "active",       // active / deleted（系统字段）
  "createdAt":     ISODate("..."), // 系统字段
  "updatedAt":     ISODate("..."), // 系统字段
  "createdBy":     "admin",        // 操作者（系统字段，来自 X-Operator header）
  "updatedBy":     "admin",        // 操作者（系统字段，来自 X-Operator header）

  // 以下为用户自定义字段（平铺到顶级）
  "name":          "Alice",
  "age":           30
}
```

**保留字段列表**（用户定义 Schema 时禁止使用）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `_id` | ObjectId | 文档主键 |
| `version` | int | 乐观锁版本号 |
| `schemaVersion` | int | 数据对应的 Schema 版本 |
| `status` | string | 软删除标记 |
| `createdAt` / `updatedAt` | date | 时间戳 |
| `createdBy` / `updatedBy` | string | 操作者 |

Schema 注册时校验字段名不与保留字段冲突。

**文档大小约束**：
- MongoDB 单文档硬限 16MB
- 应用层软限 **1MB**（在写入前校验，预留缓冲）
- Schema 注册时可限制单字段大小 + 数组长度

### 5.2 元数据集合（存储 Schema 定义 + 版本管理）

- **集合名**：`schema_definitions`

```json
{
  "_id": "type_user",
  "currentVersion": 3,
  "versions": [
    {
      "version": 1,
      "fields": {
        "name": { "type": "string", "required": true }
      },
      "createdAt": "2026-01-01T10:00:00Z",
      "createdBy": "admin"
    },
    {
      "version": 2,
      "fields": {
        "name": { "type": "string", "required": true },
        "age": { "type": "integer", "required": false }
      },
      "createdAt": "2026-02-01T10:00:00Z",
      "changeLog": "新增可选字段 age",
      "createdBy": "admin"
    },
    {
      "version": 3,
      "fields": {
        "name": { "type": "string", "required": true },
        "age": { "type": "integer", "required": true },
        "email": { "type": "string", "required": false }
      },
      "createdAt": "2026-03-01T10:00:00Z",
      "changeLog": "age 改为必填，新增 email 字段",
      "createdBy": "admin"
    }
  ],
  "updatedAt": "2026-03-01T10:00:00Z"
}
```

**版本管理规则**：
- 版本号只增不减
- 废弃版本标记 `deprecated: true`，不物理删除（数据可能仍引用旧版本）
- 字段移除采用"软删除"：标记 `deprecated`，新数据不再要求该字段

### 5.3 历史集合（记录数据全生命周期）

- **命名规范**：`col_<typeName>_history`（例如 `col_user_history`）
- **作用**：记录每一次变更的全量快照（**不可变审计日志**）

```json
{
  "_id":          ObjectId("..."),
  "docId":        "原文档的ObjectId",
  "typeName":     "user",
  "dataVersion":  5,                // 数据的版本号
  "schemaVersion": 2,               // 当时使用的 Schema 版本
  "snapshot":     { "name": "Alice", "age": 31, "status": "active" },
  "event":        "update",         // insert | update | delete
  "changedFields": ["age"],         // 本次变更字段（仅 update 有效）
  "operator":     "admin",          // 操作者（来自文档的 createdBy/updatedBy）
  "operatedAt":   "2026-07-30T10:00:00Z",
  "resumeToken":  "token_string",   // Change Stream Resume Token 原文（供查询）
  "resumeTokenHash": "a1b2c3d4e5f6a7b8"  // SHA256 前 16 字节 hex（幂等唯一键）
}
```

**关键字段说明**：
- `snapshot`：insert/update/replace 事件用 `fullDocument`；**delete 事件无 `fullDocument`**，snapshot 改存 `documentKey._id`
- `resumeToken`：Resume Token 原文，供查询和调试
- `resumeTokenHash`：幂等键，Worker 重复处理同一事件时通过唯一索引拦截（Resume Token 原文可能超过 MongoDB 索引键 1024 字节限制，用 SHA256 前 16 字节 hex）
- `operator`：从文档的 `createdBy`/`updatedBy` 字段读取（不在 Worker ctx 中）
- `changedFields`：本次变更字段列表（仅 update 有效），用于审计查询"谁改过 X 字段"时快速过滤，避免 diff 全量 snapshot

**历史集合不可变性**：
- 审计日志一旦写入不修改、不软删除
- 不设置 `isDeleted` 等软删字段（违背审计初衷）
- **永久保留，不自动过期清理**（详见容量规划 §17.2）

### 5.4 数据与 Schema 版本的关系

```
┌─────────────────┐      ┌─────────────────────────────┐
│   数据文档      │      │      Schema 定义             │
│ schemaVersion: 2│─────▶│  version: 2 的字段定义       │
│ {               │      │  {                          │
│   name: "Alice",│      │    name: string, required   │
│   age: 30       │      │    age: int, optional       │
│ }               │      │  }                          │
└─────────────────┘      └─────────────────────────────┘
```

**版本校验规则（关键决策）**：

| 操作 | Schema 来源 | 说明 |
|------|------------|------|
| **Insert** | 当前最新版本 | 新数据使用最新 Schema 校验 |
| **Update** | **数据自身的 schemaVersion** | 不强制旧数据补齐新版必填字段 |
| **Query** | 不需要 | 直接透传 |
| **Delete** | 不需要 | 软删除 |

**设计决策：schemaVersion 语义（待业务方确认）**

> 以下列出三种方案，**推荐方案 B**（下方算法按方案 B 实现）。业务方确认后若选择其他方案需相应调整算法和 §6.2/§10.2/§11.3 描述。

| 方案 | schemaVersion 语义 | Update 新版字段处理 | 优点 | 缺点 |
|------|-------------------|-------------------|------|------|
| A | 创建时版本，Update 可升级 | 允许 optional + 升级 schemaVersion | 语义严格 | 联动修订多，"部分升级"文档语义模糊 |
| **B（推荐）** | **创建时版本，Update 不改变** | **允许 optional，schemaVersion 不变** | **联动修订少，语义清晰，业界对标** | **旧文档可能含新版字段** |
| C | 数据当前符合的版本 | 拒绝所有新版字段 | 最严格 | 加 optional 字段也需迁移，可用性差 |

> 方案 B 对标 **Confluent Schema Registry** 的 `schema_id` 模式（数据携带创建时 schema id，永不改变）和 **Avro** 的 writer schema 模式。向前兼容变更（新增 optional 字段）无需迁移，破坏性变更（新增 required 字段、改类型、收紧约束）必须迁移。

**向前兼容变更 vs 破坏性变更（业界标准演化规则）**：

| 变更类型 | 举例 | 兼容性 |
|---------|------|--------|
| 新增 optional 字段 | v1 无 email，v2 加 `email`（required=false） | ✅ 向前兼容 |
| 删除 optional 字段 | v1 有 nickname（optional），v2 标记 deprecated | ✅ 向前兼容 |
| 放宽约束 | v1 `age maxLength=20`，v2 改为 `maxLength=50` | ✅ 向前兼容 |
| 新增 required 字段 | v1 无 email，v2 加 `email`（required=true） | ❌ 破坏性，需迁移 |
| 修改字段类型 | v1 `age: integer`，v2 改为 `age: string` | ❌ 破坏性，需迁移 |
| 收紧约束 | v1 `age maxLength=50`，v2 改为 `maxLength=20` | ❌ 破坏性，需迁移 |

**Update 时字段分类处理（方案 B）**：

| 变更字段类别 | 校验来源 | 处理 | schemaVersion |
|------------|---------|------|--------------|
| 当前 schemaVersion 已有字段 | 当前 schemaVersion 的 Schema | 按当前版本校验，允许 | 不变 |
| 新版 optional 字段 | 最新 Schema | 按最新版本校验该字段，**允许** | 不变 |
| 新版 required 字段 | - | **拒绝 422**（`NEW_REQUIRED_FIELD`），需走数据迁移 | - |
| 已 deprecated 字段 | - | **拒绝 422**（`FIELD_DEPRECATED`） | - |
| 不在任何版本中的字段 | - | **拒绝 422**（`FIELD_NOT_IN_SCHEMA`） | - |

**规则**：
- 写入数据时，数据文档的 `schemaVersion` 字段记录**创建时**的 Schema 版本号，Update 不改变
- 读取数据时，根据数据文档的 `schemaVersion` 查找对应的 Schema 定义来解析数据
- 不同版本的数据可以共存，应用层根据 `schemaVersion` 做差异化处理
- 旧文档可能包含比其 `schemaVersion` 更新版本的字段（通过 Update 写入的 optional 新字段），应用层需容忍此情况

**Update 字段分类校验算法**：

```
Update(typeName, docId, changedFields):
    doc = FindOne(col_<typeName>, {_id: docId})
    currentSchema = LoadSchema(typeName, doc.schemaVersion)           # 数据创建时版本
    latestSchema  = LoadSchema(typeName, currentSchema.currentVersion) # 最新版本
    currentFields = currentSchema.versions[doc.schemaVersion - 1].fields
    latestFields  = latestSchema.versions[latestSchema.currentVersion - 1].fields

    for field in changedFields.keys():
        if field in currentFields and not currentFields[field].deprecated:
            # 旧版已有字段（未废弃）：按当前版本校验
            if validateField(currentFields[field], changedFields[field]) fails:
                return 422 VALIDATION_ERROR

        else if field in latestFields and not latestFields[field].required and not latestFields[field].deprecated:
            # 新版 optional 字段（未废弃）：按最新版本校验，允许
            if validateField(latestFields[field], changedFields[field]) fails:
                return 422 VALIDATION_ERROR

        else if field in latestFields and latestFields[field].required:
            # 新版 required 字段：拒绝，需走数据迁移统一升级
            return 422 NEW_REQUIRED_FIELD

        else if field in currentFields and currentFields[field].deprecated:
            # 已废弃字段：拒绝
            return 422 FIELD_DEPRECATED

        else if field in latestFields and latestFields[field].deprecated:
            # 已废弃字段：拒绝
            return 422 FIELD_DEPRECATED

        else:
            # 字段不在任何版本中
            return 422 FIELD_NOT_IN_SCHEMA

    # 校验通过，执行乐观锁更新（schemaVersion 保持不变）
    UpdateWithOptimisticLock(docId, changedFields)
```

**规则总结**：
- `schemaVersion` 表示**创建时版本**，Update 不改变（方案 B）
- 旧版已有字段 → 按当前版本校验，允许
- 新版 optional 字段 → 按最新版本校验，允许（向前兼容，无需迁移）
- 新版 required 字段 → 拒绝 422（破坏性变更，需走数据迁移）
- 已废弃字段 → 拒绝 422
- 不在任何版本中的字段 → 拒绝 422

---

## 六、核心模块详细设计

### 6.1 类型注册中心（Registry）

- **职责**：管理所有 `col_<typeName>` 集合的懒加载创建与缓存
- **关键功能**：
  - 首次访问时自动创建集合
  - 自动建立通配符索引 `{"$**": 1}`
  - 幂等性保证（重复注册不报错，`createCollection` 已存在则忽略）

```
Registry {
    client *mongo.Client
    dbName string
    mu     sync.RWMutex
    types  map[string]*mongo.Collection  // 集合缓存
}

RegisterType(typeName) error
GetCollection(typeName) (*Collection, error)
```

**并发说明**：集合懒加载用 `sync.RWMutex` 保护内存缓存，`createCollection` 操作本身幂等（MongoDB 已存在时返回错误可忽略）。

**集合创建时机**：
- 类型注册时**同时创建** `col_<typeName>` 和 `col_<typeName>_history`
- 同时创建两个集合的所有索引（通配符、status+createdAt、TTL、resumeTokenHash 唯一、docId+operatedAt）
- 避免 Worker 首次写历史时集合不存在的边界问题

**typeName 校验规则**（注册时校验）：

| 规则 | 说明 |
|------|------|
| 字符白名单 | `^[a-z][a-z0-9_]*$`，全小写字母数字下划线 |
| 长度限制 | 3-50 字符（含 `col_` 前缀和 `_history` 后缀不超 MongoDB 120 字节限制） |
| 保留名禁止 | `system`、`admin`、`local`、`config`、`schema_definitions`、`resume_tokens`、`event_fallback` 等 |

**类型不可删除**：
- 类型一旦注册不可删除，避免误删数据
- 支持 `deprecated` 标记（在 `schema_definitions` 文档加 `deprecated: true`）
- deprecated 类型不可写入新数据，可查询历史数据

**首次注册并发**：
- `schema_definitions` 用 `_id` 唯一索引（`type_<typeName>`）
- 并发注册同 typeName 时后到者收到重复键错误，返回 409 Conflict

### 6.2 数据操作层（Repository）

| 操作 | Schema 来源 | 流程 | 并发控制 |
|------|------------|------|---------|
| **Insert** | 当前最新版本 | 读最新 Schema → 全量校验 → 写入 `col_<typeName>`，记录 `schemaVersion` + `createdBy` | MongoDB 原子操作 |
| **Query** | 不需要 | 用户 filter + 系统自动叠加 `status: "active"`（详见 6.5） → 透传给 MongoDB → 强制分页（详见 6.7） | 无 |
| **Update** | **数据自身 `schemaVersion` + 最新版本** | 读数据当前 schemaVersion + 最新版本 → 分类校验变更字段（旧版字段按旧版校验，新版 optional 字段按新版校验，详见 §5.4）→ 乐观锁更新 | 乐观锁（version） |
| **Delete** | 不需要 | 软删除：更新 `status="deleted"` → 走更新流程 | 乐观锁（version） |

**Insert 竞态说明**：Insert 流程是"读 schema_definitions 拿 currentVersion → 校验 → 写入"。若步骤 1 和 3 之间 Schema 演进到新版本，写入数据的 `schemaVersion` 是读时的版本（旧版本）。这是**合法的**，符合 5.4 多版本共存设计，无需特殊处理。

```
Insert(typeName, data, operator):
    schema = FindOne(schema_definitions, _id="type_"+typeName)  # 原子快照
    version = schema.currentVersion
    if validateFull(schema.versions[version-1], data) fails:
        return ValidationError

    doc = data ∪ {
        schemaVersion: version,
        status:        "active",
        version:       1,
        createdAt:     now,
        updatedAt:     now,
        createdBy:     operator,
        updatedBy:     operator,
    }
    InsertOne(col_<typeName>, doc)
```

### 6.3 动态校验层（Validation）

- **使用库**：`github.com/xeipuuv/gojsonschema`
- **工作流程**：
  1. 用户注册类型时，字段定义自动转换为 JSON Schema
  2. 存储到 `schema_definitions` 集合，版本号 +1
  3. 插入/更新时，根据目标版本加载对应的 Schema 进行校验
  4. 校验失败返回明确错误信息

**用户字段定义校验规则**（注册时校验）：

| 项 | 规则 |
|----|------|
| 字段名 | `^[a-z][a-z0-9_]*$`，全小写，不与保留字段冲突 |
| type 取值 | `string` / `integer` / `number` / `boolean` / `array` / `object` / `date` |
| required | bool |
| 可选约束 | `maxLength` / `minimum` / `maximum` / `pattern` / `enum`（按 type 适用） |
| 嵌套对象 | 支持（object 类型可定义子字段） |

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "name": { "type": "string", "maxLength": 20 },
    "age":  { "type": "integer", "minimum": 0 }
  },
  "required": ["name", "age"]
}
```

**Schema 缓存策略**：
- apiserver 内存缓存 Schema 定义（避免每次 Insert/Update 都查 `schema_definitions`）
- TTL 60s 自动过期，下次访问时重新加载
- Schema 变更后最多延迟 60s 感知（可接受，Schema 变更是低频操作）
- 缓存 key：`typeName + version`
- 简单可靠，无需 Change Stream 通知失效

### 6.4 Schema 版本演进流程

```
用户提交 Schema 变更请求
    ↓
FindOneAndUpdate 原子操作：
    - $inc: currentVersion
    - $push: versions[]（新版本）
    ↓
（可选）后台数据迁移：将旧版本数据升级到新版本
```

**演进策略**：

| 策略 | 说明 | 适用场景 |
|------|------|---------|
| **向前兼容（推荐）** | 新增 optional 字段/放宽约束时旧数据无需迁移即可读写；新增 required 字段/收紧约束/改类型属破坏性变更，必须迁移（详见 §5.4 演化规则） | 零停机演进，生产环境首选 |
| **数据迁移** | 后台脚本将旧版本数据批量升级到新版本 | 数据结构重大变更，需要统一格式 |

**并发说明**：
- `FindOneAndUpdate` 是原子操作，两个并发请求都会成功，版本号依次 +1（1→2→3）
- 后到者基于请求时提交的 fields **全量定义新版本**（非字段级 merge），手动填写 changeLog
- **注意**：并发 Schema 演进时，后到者的 fields 会完整定义新版本，不自动继承前者的字段变更。若管理员 A 加 `email` 字段（v2），管理员 B 同时加 `phone` 字段（v3），则 v3 只有 `phone`，**`email` 不会自动合并**。B 需在提交时包含 A 的字段变更，或串行执行
- 建议：Schema 演进是低频操作，约定串行执行（前端加锁或操作前拉取最新版本确认）

**版本不可删除**：
- 版本号只增不减
- 废弃版本标记 `deprecated: true`，不物理删除
- 数据可能仍引用旧版本，物理删除会导致数据无法解析

### 6.5 软删除状态机（严格模式）

```
                Update
   active ────────────────> active
     │
     │ Delete
     ▼
   deleted ──── Update ──> 拒绝（409 Conflict）
     │
     │ Delete
     ▼
   deleted（幂等返回成功）
```

| 操作 | 软删除前（active） | 软删除后（deleted） |
|------|------------------|-------------------|
| Update | 允许 | 拒绝（409） |
| Delete | 软删除 → deleted | 幂等返回成功 |
| Query | 默认可见 | 默认排除，需 `?include_deleted=true` 显式查询 |

**Query 软删除过滤规则**（统一说明，6.2/6.7 引用此处）：
- 系统自动在用户 filter 上叠加 `status: "active"` 过滤
- 用户传 `?include_deleted=true` 时才查 deleted 数据
- 用户 filter 禁止包含 `status` 字段（系统字段，详见 6.7 字段白名单）

### 6.6 数据迁移任务设计

```
cmd/migrate --type=user --from=1 --to=3
    ↓
分批处理（每批 1000 条，间隔 100ms）：
    1. 查询 schemaVersion=from 的文档（按 _id 排序，断点续传）
    2. 应用迁移规则（字段补全 / 格式转换）
    3. UpdateOne 更新文档 + schemaVersion
    4. 记录 last_processed_id 到 migrate_progress 集合
    ↓
失败可重试（幂等），不回滚已迁移数据
```

**迁移任务说明**：
- 手动执行，单实例运行（运维操作，无需分布式锁）
- 迁移过程不阻塞业务写入（向前兼容策略保证）
- 迁移进度持久化，支持断点续传

**迁移期间事件风暴控制**：
- 迁移产生大量 Change Stream 事件，每个走 Asynq + 历史写入
- **迁移任务使用独立 Asynq 队列**（`activelist-migrate`），避免阻塞业务事件处理
- 分批大小控制（每批 1000 条，间隔 100ms）避免洪流
- 监控迁移队列长度，积压 > 50000 时暂停迁移（业务队列阈值 10000 不变）
- 迁移期间设置告警静默窗口，避免迁移队列积压触发 P1 告警风暴
- 历史集合会暴增（迁移 N 条 = N 条历史），需提前评估磁盘

**迁移与 Schema 演进互斥**：
- 迁移期间禁止 Schema 变更（apiserver 检测到该 typeName 有进行中的迁移任务时返回 409）
- 迁移工具启动时在 `migrate_progress` 集合记录 `{typeName, status: "running", from, to, heartbeat: now()}`
- 迁移工具每 30s 更新 `heartbeat` 字段
- apiserver Schema 演进前检查 `migrate_progress`：`status == "running"` 且 `heartbeat` 在 5min 内 → 返回 409
- `heartbeat` 超时 5min（10 倍心跳间隔，足够容忍 GC/慢查询）→ apiserver **自动判定为僵尸迁移**，将 `status` 改为 `"abandoned"`，允许 Schema 变更，并触发 P1 告警
- 迁移完成后更新 `migrate_progress` 为 `{status: "done"}`
- 迁移过程中 `--to` 版本固定，即使其他 typeName 的 Schema 变更也不影响当前迁移

**僵尸迁移恢复流程**（对标 Flyway `repair` / etcd lease 过期自动释放）：
1. 迁移工具崩溃 → `heartbeat` 停止更新
2. 5min 后 apiserver 自动将 `status` 改为 `"abandoned"` + P1 告警
3. Schema 变更能力自动恢复（无需人工介入）
4. 迁移工具重启时检查 `status`：
   - `"abandoned"` → 提示运维确认（需 `--force` 才能继续，避免与误判冲突）
   - `"running"` → 正常情况不应出现（说明 heartbeat 未过期），拒绝启动
5. 提供 `cmd/migrate cleanup --type=<typeName>` 工具手动清理 `migrate_progress` 记录（兜底手段）

**安全保证**：
- 5min TTL 是 10 倍心跳间隔，误判概率极低
- 即使误判（迁移实际还在运行），迁移工具用 `--to` 固定版本，不会写错版本数据
- `abandoned` 状态的迁移重启时需 `--force` 确认，双重保险

### 6.7 查询安全策略

Query 接口不能完全透传用户 filter，需做安全处理：

**字段白名单**：
- 只允许查询用户自定义字段 + `_id`
- 禁止查询系统字段：`version`、`schemaVersion`、`status`、`createdAt`、`updatedAt`、`createdBy`、`updatedBy`
- `status` 由系统自动叠加（详见 6.5 软删除过滤规则）

**操作符黑名单**：
- 禁止 `$where`、`$expr`、`$function`、`$accumulator`（防注入 + 防 DoS）
- 允许 `$eq`、`$ne`、`$gt`、`$gte`、`$lt`、`$lte`、`$in`、`$nin`、`$regex`、`$exists`、`$and`、`$or`

**强制分页**：
- 默认 `page_size=20`
- `page_size` 上限 100
- 不传 `page` 默认第 1 页

**响应格式**：
```json
{
  "list": [...],
  "total": 1234,
  "page": 1,
  "page_size": 20
}
```

### 6.8 错误码规范

**HTTP 状态码**：

| 状态码 | 场景 |
|--------|------|
| 200 | 成功 |
| 201 | 创建成功 |
| 400 | 参数错误（JSON 解析失败、缺必填参数） |
| 404 | 资源不存在（类型/文档/Schema 版本） |
| 409 | 冲突（类型已存在、文档已删除、Schema 变更冲突） |
| 422 | 校验失败（字段类型错误、必填缺失、保留字段冲突、改新版 required 字段、改已废弃字段） |
| 429 | 限流（未来扩展） |
| 500 | 内部错误（MongoDB 异常、未知错误） |
| 503 | 依赖不可用（MongoDB/Redis 不可达） |

**响应体格式**（统一 `{code, msg, data}` 包装，与 zhuzhao 网关格式一致）：
```json
{
  "code": 422,
  "msg": "字段 age 类型错误，期望 integer 实际 string",
  "data": null,
  "detail": {
    "error_code": "VALIDATION_ERROR",
    "field": "age",
    "expected": "integer",
    "actual": "string"
  }
}
```

> `code` 为整数 HTTP 状态码（与 zhuzhao 一致），字符串错误码移到 `detail.error_code`。成功响应 `data` 为业务数据，错误响应 `data` 为 `null`，`detail` 为可选的错误详情。

**错误码常量**（`code` 字段）：

| code | 说明 |
|------|------|
| `VALIDATION_ERROR` | Schema 校验失败 |
| `RESERVED_FIELD` | 字段名与保留字段冲突 |
| `TYPE_NOT_FOUND` | 类型不存在 |
| `TYPE_ALREADY_EXISTS` | 类型已存在 |
| `TYPE_DEPRECATED` | 类型已废弃，不可写入 |
| `DOC_NOT_FOUND` | 文档不存在 |
| `DOC_DELETED` | 文档已删除 |
| `SCHEMA_VERSION_NOT_FOUND` | Schema 版本不存在 |
| `FIELD_NOT_IN_SCHEMA` | 字段不在任何 Schema 版本中 |
| `NEW_REQUIRED_FIELD` | 新版 required 字段，需走数据迁移统一升级 |
| `FIELD_DEPRECATED` | 字段已废弃，不可修改 |
| `CONFLICT` | 并发冲突，请重试 |
| `INTERNAL_ERROR` | 内部错误 |
| `DEPENDENCY_UNAVAILABLE` | 依赖服务不可用 |

### 6.9 API 接口清单

**类型管理（admin）**：

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/v1/admin/types` | 注册新类型 |
| GET | `/api/v1/admin/types` | 查所有类型列表 |
| GET | `/api/v1/admin/types/:typeName` | 查类型当前 Schema 定义 |
| GET | `/api/v1/admin/types/:typeName/history` | 查类型 Schema 变更历史 |
| POST | `/api/v1/admin/types/:typeName/schema` | Schema 版本演进 |
| POST | `/api/v1/admin/types/:typeName/deprecate` | 废弃类型 |

**数据 CRUD**：

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/v1/data/:typeName` | 插入数据 |
| GET | `/api/v1/data/:typeName` | 列表查询（分页，详见 6.7） |
| GET | `/api/v1/data/:typeName/:id` | 查询单个文档 |
| POST | `/api/v1/data/:typeName/:id/update` | 更新文档（2026-09-09 实现路由形态） |
| POST | `/api/v1/data/:typeName/:id/delete` | 软删除文档（同上） |
| GET | `/api/v1/data/:typeName/:id/history` | 查文档变更历史 |

**响应格式**（统一 `{code, msg, data}` 包装，与 zhuzhao 网关格式一致）：
- 单文档：`{ "code": 200, "msg": "success", "data": { "_id": "...", "name": "...", ... } }`（data 含系统字段）
- 列表：`{ "code": 200, "msg": "success", "data": { "list": [...], "total": 1234, "page": 1, "page_size": 20 } }`（详见 6.7）
- 历史：`{ "code": 200, "msg": "success", "data": { "list": [...], "total": 1234, "page": 1, "page_size": 20 } }`（每条含 snapshot/event/operator/operatedAt）
- 错误：`{ "code": 422, "msg": "...", "data": null, "detail": { "error_code": "VALIDATION_ERROR", ... } }`（详见 6.8）

---

## 七、高可靠性事件驱动架构

### 7.1 架构分层

```
数据变更发生（API / Shell / 其他程序）
    ↓
Change Stream 监听器（数据库级别监听）
    - 识别集合名（如 col_user）和操作类型
    - 拼装任务类型：<typeName>:<operation>
    - 投递 Asynq 任务（仅搬运，零业务逻辑）
    - 持久化 Resume Token
    - Redis 故障时写入 fallback 表
    ↓
Asynq 任务队列（持久化到 Redis）
    - 支持失败重试 + 指数退避
    - 支持死信队列（DLQ）
    ↓
Asynq Worker（任务处理器）
    ┌─ 中间件 Handler（所有任务经过） ─┐
    │  1. 历史 Handler：写 col_<typeName>_history（幂等） │
    │  2. 业务 Handler：按 <typeName>:<operation> 路由     │
    └────────────────────────────────┘
```

### 7.2 Change Stream 监听器（事件捕获层）

- **监听粒度**：数据库级别（`db.Watch()`），自动覆盖所有 `col_<typeName>`
- **前置条件**：MongoDB 副本集模式
- **职责**：仅做事件识别、类型拼接、任务投递，**绝不处理业务逻辑**

```
WatchDatabase(db, asynqClient):
    token = LoadResumeToken()  # 从 resume_tokens 集合加载
    opts = ChangeStreamOptions{
        FullDocument: UpdateLookup,  # 关键：update 事件返回更新后全文
        ResumeAfter:  token,         # 断点续传
    }

    for {
        stream = db.Watch(opts)
        for stream.Next(ctx) {
            event = decode(stream)

            # 安全取值（避免 panic）
            opType = event.operationType      # insert/update/delete/replace/...
            collName = event.ns.coll
            typeName = TrimPrefix(collName, "col_")

            # drop/dropDatabase/rename/create/modify 等非数据事件忽略 + 告警
            if opType not in ["insert", "update", "delete", "replace"]:
                Log("ignore event: " + opType)
                SaveResumeToken(stream.ResumeToken())
                continue

            # delete 事件无 fullDocument，用 documentKey
            snapshot = event.fullDocument or event.documentKey

            taskType = typeName + ":" + opType
            payload  = marshal({
                snapshot:      snapshot,
                docId:         event.documentKey._id,
                changedFields: event.updateDescription.updatedFields,
                resumeToken:   stream.ResumeToken(),
            })

            # 投递 Asynq
            try:
                Enqueue(asynqClient, taskType, payload)
                SaveResumeToken(stream.ResumeToken())  # 投递成功后才保存
            except RedisUnavailable:
                WriteFallback(taskType, payload, stream.ResumeToken())
                SaveResumeToken(stream.ResumeToken())
        }

        # 内层循环退出（stream 关闭或 invalidate 事件）
        if stream.Err():
            Log(stream.Err())
            Sleep(backoff())  # 指数退避
            # 用最后 token 重新 Watch（外层 for 会重新执行）
        else:
            # stream.Next 返回 false 但无错误，通常是 invalidate 事件
            # stream 永久关闭，清空 token 重建
            Log("stream invalidated, rebuild without token")
            token = nil
    }
```

**resume_tokens 集合文档结构**：

```json
{
  "_id": "watcher_1",
  "token": "base64_token_string",
  "updatedAt": "2026-08-03T10:00:00Z"
}
```

- 单 watcher 时一条记录（`_id` 固定为 `watcher_1`）
- 多 watcher 选主时仍一条（只有 leader 写）
- 每次 `SaveResumeToken` 用 `UpdateOne` upsert 覆盖

**关键技术点**：

| 问题 | 处理方式 |
|------|---------|
| `delete` 事件无 `fullDocument` | snapshot 改存 `documentKey._id` |
| `update` 事件默认无全文 | `SetFullDocument(UpdateLookup)` |
| Resume Token 持久化 | 投递 Asynq 成功后写入 `resume_tokens` 集合 |
| stream 错误重连 | 指数退避重连，用最后 token 续传 |
| `invalidate` 事件 | stream 永久关闭，清空 token 重建 |
| `drop`/`dropDatabase`/`rename`/`create`/`modify` 事件 | 忽略 + 告警（非数据变更） |
| oplog 窗口过期 | token 失效时报错并告警，人工介入（从最后已知 token 重新开始或接受事件丢失） |

### 7.3 Asynq Worker（任务处理层）

**Asynq 限制说明**：`asynq.ServeMux` 是**精确匹配**（内部 `map[string]Handler`），**不支持通配符**。原方案的 `mux.HandleFunc("*:*", ...)` 永远不会被匹配到。

**正确做法：中间件模式**

```
# 自定义中间件 Handler
MiddlewareHandler implements asynq.Handler:
    historyHandler Handler
    businessMux    *ServeMux

    ProcessTask(ctx, t):
        # 1. 先跑历史 Handler（幂等，重复键忽略）
        if err = historyHandler.ProcessTask(ctx, t); err != nil:
            return err  # 历史失败触发 Asynq 重试

        # 2. 再按 type 路由业务 Handler（可选）
        if handler, ok = businessMux.Lookup(t.Type()); ok:
            return handler.ProcessTask(ctx, t)
        return nil  # 无业务 Handler 也算成功（历史已记录）


# Worker 启动
srv = NewServer(RedisOpt{...}, Config{Concurrency: 10})
mux = NewServeMux()

# 业务处理器按需注册（可后期扩展）
mux.HandleFunc("user:insert",      handleUserInsert)
mux.HandleFunc("product:update",   handleProductUpdate)

# 历史处理器作为中间件包装
historyHandler = NewHistoryHandler(db)
srv.Run(MiddlewareHandler{historyHandler, mux})
```

**历史 Handler 实现**：

```
handleWriteHistory(ctx, t):
    payload = unmarshal(t.Payload())

    # 解析任务类型
    parts = split(t.Type(), ":")
    typeName, event = parts[0], parts[1]

    # 确保历史集合存在（类型注册时已创建，此处兜底懒加载）
    EnsureHistoryCollection(typeName)

    # resumeToken 可能过长，用 hash 作为唯一索引键
    tokenHash = sha256(payload.resumeToken)[:16].hex()

    history = {
        docId:           payload.docId,
        typeName:        typeName,
        dataVersion:     payload.snapshot.version,
        schemaVersion:   payload.snapshot.schemaVersion,
        snapshot:        payload.snapshot,
        event:           event,
        changedFields:   payload.changedFields,
        operator:        payload.snapshot.updatedBy or payload.snapshot.createdBy,
        operatedAt:      now(),
        resumeToken:     payload.resumeToken,  # 原文供查询
        resumeTokenHash: tokenHash,            # hash 供唯一索引
    }

    try:
        InsertOne(col_<typeName>_history, history)
    except DuplicateKeyError:
        return nil  # 幂等：重复处理同一事件，忽略
    except OtherError:
        return err  # 触发 Asynq 重试
```

**幂等保证**：历史集合 `{resumeTokenHash: 1}` 唯一索引（Resume Token 原文可能超过 MongoDB 索引键 1024 字节限制，用 SHA256 前 16 字节 hex 作为索引键）。

**顺序保证**：
- Asynq 不保证 FIFO，同一文档的多次 Update 可能乱序到达 Worker
- 历史快照各自完整（每次 Update 都带全量 snapshot），乱序不影响单条快照的可读性
- 前端展示历史时按 `operatedAt` 排序，不依赖写入顺序

**业务 Handler 幂等性要求**：
- 历史 Handler 由 `resumeTokenHash` 唯一索引保证幂等（重复处理同一事件自动忽略）
- **业务 Handler 必须自行实现幂等性**，Asynq 本身不保证 Exactly-Once 语义
- 重复投递场景：Enqueue 成功 + SaveResumeToken 失败 → watcher 重启后用旧 token 重新 Watch → 同一事件被再次 Enqueue
- 幂等实现建议：
  - 数据库操作：用 `Upsert` 或条件更新（如 `if not exists`）
  - 外部副作用（发邮件/通知）：用去重表或 Redis `SETNX` 标记已处理
  - 累加操作：改用幂等的"设置绝对值"而非"增量更新"
- 无业务 Handler 时（`return nil`），历史已记录即算成功，无幂等风险

**DLQ 处理**：
- Asynq 重试 N 次（默认 25 次，可配置）后进 DLQ
- DLQ 长度告警，运维介入
- 人工补单：从 fallback 表重新投递，或从 oplog 手动重放关键事件

**Worker 优雅停止**：
- 接到 SIGTERM 时调用 `srv.Shutdown()`
- 等待当前正在处理的任务完成
- 超时 30s 后强制退出
- 未处理的任务留在 Asynq 队列中，下次启动继续消费

**apiserver 优雅停止**：
- 接到 SIGTERM 时调用 `http.Server.Shutdown()`
- 等待当前正在处理的 HTTP 请求完成
- 超时 30s 后强制退出
- 拒绝新请求，返回 503

### 7.4 Redis 故障 fallback 机制

```
apiserver 写入数据 → Change Stream 捕获事件
                            ↓
                尝试 Enqueue Asynq
                            ↓
                ┌────成功────────────失败（Redis 故障）┐
                │                                      │
                ▼                                      ▼
        SaveResumeToken                      写入 event_fallback 集合：
                                              {
                                                taskType, payload,
                                                resumeToken,
                                                status: "pending",
                                                createdAt: now(),
                                                retryCount: 0
                                              }
                                            SaveResumeToken
                │                                      │
                │                                      ▼
                │              后台 goroutine 定期扫描：
                │                - 查 status=pending 的记录
                │                - 尝试重投到 Asynq
                │                - 成功 → 删除 fallback 记录
                │                - 失败 → retryCount++，下次重试
                │                                      │
                └──────────────────────────────────────┘
```

**fallback 表设计**：
- 集合名：`event_fallback`
- 索引：`{status:1, createdAt:1}`（重投扫描）
- TTL：不设置（兜底数据必须可靠，不能自动过期）
- 监控：fallback 表长度 > 0 时告警（说明 Redis 有问题）

### 7.5 Watcher 高可用方案（分阶段）

**初期（开发/测试环境）：单副本部署**
- K8s `replicas=1` 或 Docker 单容器，依赖 Resume Token 续传
- Pod 重启窗口期事件靠 oplog 兜底（oplog 窗口至少 24h）
- 简单可靠，适合初期验证

**正式环境：多副本 + 选主**
- K8s `replicas=2+`，Redis Redlock 选主（TTL 30s，心跳 10s，故障切换 < 30s）
- 或 Mongo `findAndModify` 心跳表选主（避免 Redis 依赖）
- 同一时刻仅 leader 监听 Change Stream，follower 待命
- leader 故障后 follower 抢锁成为新 leader，从 `resume_tokens` 加载最后 token 续传

**节假日预案**：
- 节假日前将 oplog 窗口扩容至 72h（`--oplogSize 71680`，约 70GB）
- 监控 oplog 剩余窗口，<12h 时 P1 告警
- 节假日期间 watcher 增加 healthcheck 频率（30s 一次）

**Watcher down 时长告警**：
- P1 告警：watcher 进程 down 持续 > 1h
- P0 告警：watcher 进程 down 持续 > 12h（接近 oplog 窗口上限，需立即人工介入）

---

## 八、高可用基础设施部署

### 8.1 MongoDB 3 节点副本集

| 配置项 | 说明 |
|--------|------|
| **架构** | 1 主 + 2 从（P-S-S） |
| **容错能力** | 容忍 1 个节点故障 |
| **部署要求** | 3 个节点分布在不同物理机/可用区 |
| **写关注** | `w: "majority"`，确保数据持久性 |
| **读偏好** | 根据业务场景配置（默认从主节点读） |
| **Journaling** | 必须开启（默认开启） |
| **oplog 窗口** | 至少 24 小时（监控 oplog 大小，不足时扩容） |

**客户端连接配置**：

```
mongodb://user:pass@host1:27017,host2:27017,host3:27017/?replicaSet=rs0&retryWrites=true&w=majority
```

- `retryWrites=true` — 故障切换期间写入失败自动重试
- `w=majority` — 写入需多数派确认
- Change Stream 在故障切换时自动重连

**故障切换期间行为**：
- 写入失败 → 客户端自动重试（`retryWrites`）
- Change Stream → 自动重连，使用 Resume Token 续传
- 读取 → 可能短暂返回旧数据（取决于读偏好）

**元数据备份**：
- `schema_definitions` 是核心元数据，丢失会导致数据无法解析
- 每日 `mongodump schema_definitions` 到备份存储
- 副本集本身提供数据冗余，备份是额外保险

### 8.2 Redis 部署

**初期**：单点 Redis + AOF 持久化
- 简单部署，依赖 fallback 表兜底（见 7.4）
- AOF `appendfsync everysec` 保证任务持久化
- 适合初期、可接受短暂不可用

**生产环境（按需升级）**：主从 + 哨兵
- 1 主 + 2 从（3 个数据节点）
- 至少 3 个哨兵（奇数），自动故障转移
- 容忍 1 个数据节点 + 1 个哨兵节点故障

**故障期间行为**：
- apiserver 投递 Asynq 任务失败 → 走 fallback 表（见 7.4）
- Worker 拿不到任务 → 等待 Redis 恢复
- 恢复后 Asynq 自动重连，从队列继续消费

### 8.3 基础设施节点总览

| 组件 | 集群模式 | 节点数量 | 说明 |
|------|---------|---------|------|
| **MongoDB** | 副本集 | 3 节点（1 主 + 2 从） | 存储业务数据 + 提供 Change Streams |
| **Redis** | 初期单点，生产主从+哨兵 | 初期 1 节点，生产 3 数据 + 3 哨兵 | Asynq 任务队列后端 |

> **说明**：MongoDB 和 Redis 是两套独立的集群，物理上建议分离部署（不同物理机/虚拟机），逻辑上完全解耦。

---

## 九、索引策略

| 索引类型 | 适用集合 | 创建方式 | 说明 |
|---------|---------|---------|------|
| **通配符索引** `{"$**": 1}` | `col_<typeName>` | 类型注册时懒加载创建 | 兜底索引，高频字段需手动建索引 |
| **精准字段索引** `{name: 1}` | `col_<typeName>` | 根据业务需求手动添加 | 高频查询字段必须 |
| **复合索引** `{status:1, createdAt:-1}` | `col_<typeName>` | 类型注册时创建 | 状态+时间排序查询 |
| **无 TTL（永久保留）** | `col_<typeName>_history` | 历史集合永久保留，不创建 TTL 索引 | 审计数据永久可查，磁盘定期扩容（详见 §17.2） |
| **唯一索引** `{resumeTokenHash: 1}` | `col_<typeName>_history` | 类型注册时创建 | 幂等保证，重复处理同一事件时拦截 |
| **复合索引** `{docId:1, operatedAt:-1}` | `col_<typeName>_history` | 类型注册时创建 | 按文档查历史，按时间排序 |
| 默认索引 `{_id: 1}` | `schema_definitions` | MongoDB 默认 | type 唯一 |
| 默认索引 `{_id: 1}` | `resume_tokens` | MongoDB 默认 | watcher 唯一 |
| 复合索引 `{status:1, createdAt:1}` | `event_fallback` | 启动时创建 | 重投扫描 |

**通配符索引性能说明**：
- 通配符索引是兜底，**不能依赖**
- 不支持 `$or`/`$nor` 顶层
- 复杂查询时优化器可能不用通配符索引
- 高频查询字段必须手动建索引
- 启用慢查询监控，迭代优化

---

## 十、并发控制策略

### 10.1 操作场景总览

| 操作场景 | 推荐方案 | 说明 |
|---------|---------|------|
| 普通数据更新（用 `$set`） | MongoDB 原子操作 | 不需要额外加锁 |
| 普通数据更新（先读后写） | 乐观锁（version 字段） | 更新时校验 version，失败重试 |
| **Schema 变更** | `FindOneAndUpdate` 原子操作 | 版本号依次 +1，后到者全量定义新版本（非字段级 merge），建议串行执行 |
| 首次创建索引 | 忽略 `already exists` 错误 | 利用 MongoDB 幂等性 |
| **Watcher 多副本** | 待定（单副本 + Resume Token 续传） | 未来按需评估选主方案 |
| **数据迁移任务** | 手动单实例执行 | 运维操作，无需分布式锁 |
| Resume Token 更新 | 单 watcher 写入 | 无并发 |
| 历史快照写入 | 唯一索引保证幂等 | 无需锁 |
| 集合懒加载 | `createCollection` 幂等 | 无需锁 |

### 10.2 乐观锁实现

```
UpdateWithOptimisticLock(id, data, operator):
    # 1. 读取当前文档
    doc = FindOne(col_<typeName>, {_id: id})
    if doc.status == "deleted":
        return ConflictError("文档已删除")

    # 2. 分类校验（按数据自身 schemaVersion + 最新版本，详见 §5.4 算法）
    #    - 旧版已有字段 → 按当前版本校验
    #    - 新版 optional 字段 → 按最新版本校验
    #    - 新版 required 字段 → 拒绝 422（需数据迁移）
    if validateBySchemeRules(doc, data) fails:
        return ValidationError

    # 3. 条件更新（必须包含当前 version）
    filter = {_id: id, version: doc.version, status: "active"}
    update = {
        $set: data ∪ {updatedAt: now, updatedBy: operator},
        $inc: {version: 1}
    }
    result = UpdateOne(filter, update)
    if result.MatchedCount == 0:
        return ConflictError("并发冲突，请重试")  # 触发客户端重试
```

**重试流程**：
- `MatchedCount == 0` 表示文档已被其他请求修改（version 不匹配）或已删除
- 重试时**重新走完整流程**：读文档 → 加载 Schema → 校验 → 条件更新
- 不是只重试 UpdateOne（否则会用旧的 version 校验结果写入新版本数据）

### 10.3 乐观锁重试策略

| 项 | 值 |
|----|----|
| 最大重试次数 | 3 次 |
| 重试间隔 | 指数退避（10ms / 20ms / 40ms） |
| 失败后 | 返回 409 Conflict |
| 防惊群 | 间隔随机抖动（±20%） |

### 10.4 跨集合事务决策

**明确不使用 MongoDB 跨集合事务**，理由：

| 场景 | 方案 | 理由 |
|------|------|------|
| 主数据写入 | 单文档原子 | MongoDB 单文档操作天生原子 |
| 历史快照 | 异步队列 + 幂等 | 解耦主流程，最终一致 |
| Schema 变更 | `FindOneAndUpdate` 原子 | 单文档操作 |
| 数据迁移 | 分批 + 幂等 + 断点续传 | 不需要跨集合原子 |

**权衡**：
- 优势：高吞吐、无锁竞争、单点故障不影响主流程
- 代价：历史快照可能延迟秒级落盘，依赖幂等保证最终一致

---

## 十一、全生命周期管理总结

### 11.1 数据全生命周期

```
创建 (Insert)
    ↓
变更 (Update) ──→ 每次变更记录快照到 history 表
    ↓
变更 (Update) ──→ 每次变更记录快照到 history 表
    ↓
软删除 (status="deleted") ──→ 记录删除事件到 history 表
    ↓
软删除数据保留（不自动物理删除）
```

**物理删除方案**：
- 不提供物理删除 API（避免误删）
- 软删除数据永久保留，或由运维手动执行清理工具
- 历史集合永久保留（不自动过期，定期扩容，详见 §17.2）
- 如需清理软删除数据，提供 `cmd/cleanup` 工具按条件批量清理（手动执行）

### 11.2 Schema 全生命周期

```
版本 1 创建
    ↓
需求变更 → 创建版本 2（向前兼容）
    ↓
需求变更 → 创建版本 3（向前兼容）
    ↓
字段废弃 → 标记 deprecated（不物理删除）
    ↓
（可选）后台数据迁移 → 统一升级到最新版本
```

### 11.3 数据与 Schema 版本协同

| 场景 | 处理方式 |
|------|---------|
| **写入新数据** | 使用当前最新 Schema 版本 |
| **读取历史数据** | 根据数据自带的 `schemaVersion` 查找对应 Schema 解析 |
| **更新已有数据** | 按数据自身 `schemaVersion` + 最新版本分类校验（详见 §5.4），`schemaVersion` 保持不变（方案 B） |
| **数据迁移** | 后台脚本升级数据格式，同时更新 `schemaVersion` |

### 11.4 全生命周期管理价值

- **审计追溯**：可查询任意时刻数据状态和对应的 Schema 定义
- **零停机演进**：新增/修改字段不影响旧数据
- **数据回滚**：可根据历史快照恢复到任意时间点
- **合规性**：满足行业对数据变更的审计要求

---

## 十二、可靠性保障矩阵

| 机制 | 实现方式 | 保障能力 |
|------|---------|---------|
| **事件不丢失（oplog 窗口内）** | Change Stream 基于 Oplog + Resume Token 持久化到 `resume_tokens` 集合 | watcher 重启后断点续传；oplog 过期时 token 失效，事件丢失（需扩大 oplog 或保障 watcher 可用，详见 §7.2/§7.5） |
| **任务不丢失** | Asynq 持久化到 Redis + Redis 故障时 fallback 表 | Worker 崩溃后任务不丢失，Redis 故障时事件兜底 |
| **失败重试** | Asynq 内置重试 + 指数退避 | 网络抖动或下游不可用时自动重试 |
| **死信处理** | Asynq DLQ + 告警 + 人工补单工具 | 失败任务保留现场供人工介入 |
| **最终一致性** | 任务队列 + 幂等保证事件最终被处理 | 重复处理同一事件被唯一索引拦截 |
| **数据高可用** | MongoDB 3 节点副本集（`w:majority`） | 容忍 1 个节点故障 |
| **队列高可用** | Redis（初期单点 + fallback 表兜底，生产可升级主从哨兵） | 容忍 Redis 短暂不可用，事件不丢 |
| **并发安全** | 乐观锁（version 字段）+ Schema 原子变更 | 防止 Schema 修改和数据更新冲突 |
| **数据审计** | 数据历史表（不可变）+ 版本化 Schema | 完整的全生命周期追溯 |
| **元数据备份** | 每日 `mongodump schema_definitions` | 元数据丢失可恢复 |
| **可观测性** | asynqmon + 日志告警（Prometheus 未来扩展） | 监控队列长度、失败率、历史缺失率 |

---

## 十三、典型流程示例

### 流程一：用户注册新类型

```
1. 管理员提交新类型定义
   POST /api/v1/admin/types    （由 zhuzhao 鉴权后透传）
   Header: X-Operator: admin
   {
     "name": "user",
     "fields": {
       "name": { "type": "string", "required": true },
       "age":  { "type": "integer", "required": false }
     }
   }

2. activelist 后端处理
   ├── 校验 typeName（字符白名单 + 长度 + 保留名）
   ├── 校验字段名不与保留字段冲突
   ├── 生成 JSON Schema
   ├── 存入 schema_definitions：
   │   {
   │     "_id": "type_user",
   │     "currentVersion": 1,
   │     "versions": [{ "version": 1, "fields": {...}, "createdBy": "admin" }]
   │   }
   ├── 懒加载创建 col_user（含通配符索引 + status/createdAt 复合索引）
   └── 返回注册成功
```

### 流程二：插入数据

```
1. 用户插入数据
   POST /api/v1/data/user    （由 zhuzhao 鉴权后透传）
   Header: X-Operator: alice
   {"name": "Alice", "age": 30}

2. activelist 后端处理
   ├── 读取 schema_definitions 获取最新 Schema（version=1）
   ├── gojsonschema 校验通过
   ├── 写入 col_user：
   │   {
   │     "_id": "...",
   │     "name": "Alice",
   │     "age": 30,
   │     "schemaVersion": 1,
   │     "version": 1,
   │     "status": "active",
   │     "createdAt": "...",
   │     "updatedAt": "...",
   │     "createdBy": "alice",
   │     "updatedBy": "alice"
   │   }
   └── 返回成功

3. Change Stream 捕获（异步）
   ├── 监听到 col_user 的 insert 事件（含 fullDocument）
   ├── 拼装任务 "user:insert"
   ├── 投递 Asynq
   └── 保存 Resume Token

4. Asynq Worker（异步，中间件模式）
   ├── 1. 历史 Handler：写入 col_user_history
   │      {
   │        docId: ..., schemaVersion: 1, snapshot: {...},
   │        event: "insert", operator: "alice", operatedAt: ...,
   │        resumeToken: "..."
   │      }
   └── 2. 业务 Handler（如注册）：发送欢迎邮件等
```

### 流程三：Schema 演进（含并发场景）

```
1. 管理员 A 提交 Schema 变更
   POST /api/v1/admin/types/user/schema
   {
     "fields": {
       "name": { "type": "string", "required": true },
       "age":  { "type": "integer", "required": true },
       "email":{ "type": "string", "required": false }
     },
     "changeLog": "age 改为必填，新增 email 字段"
   }

2. 同一时刻管理员 B 提交另一个变更
   ├── A: FindOneAndUpdate $inc currentVersion (1→2), $push v2（原子操作）
   ├── B: FindOneAndUpdate $inc currentVersion (2→3), $push v3（原子操作）
   └── 两个请求都成功，串行执行，无需重试

3. 效果
   ├── 新数据插入：使用 v3 的 Schema 校验（age 必填）
   ├── 旧数据查询：根据 schemaVersion=1 使用旧 Schema 解析
   └── 历史数据：保持不变，各有对应的 Schema 版本
```

### 流程四：数据回滚

```
1. 发现数据异常（age 被误改为 200）
   ├── 查询 col_user_history 获取该文档的历史快照
   └── 找到误修改前的版本（age=30）

2. 执行回滚
   ├── 将快照数据写回 col_user（Update + 乐观锁）
   ├── 版本号 +1
   └── 历史表自动记录回滚事件（Change Stream 捕获）
```

### 流程五：Watcher 重连与 Resume Token 续传

```
1. 网络抖动导致 stream 关闭
   ├── stream.Err() 返回错误
   ├── 记录日志，指数退避（1s/2s/4s/8s/16s，上限 60s）
   ├── 从 resume_tokens 加载最后 token
   └── 用 token 重新 Watch

2. Resume Token 失效（oplog 已被覆盖）
   ├── Watch 返回 "resume of change stream was not possible"
   ├── 报错并告警
   └── 人工介入：从最后已知 token 重新开始或接受事件丢失

3. invalidate 事件
   ├── stream 永久关闭（如集合被删除）
   ├── 清空 resume_tokens
   └── 重新开始 Watch（不带 token）
```

### 流程六：Redis 故障 fallback 工作流程

```
1. 数据写入 col_user → Change Stream 捕获事件

2. 尝试 Enqueue Asynq → 失败（Redis 不可用）

3. 写入 event_fallback 集合
   {
     taskType: "user:insert",
     payload: {...},
     resumeToken: "...",
     status: "pending",
     createdAt: now(),
     retryCount: 0
   }

4. 保存 Resume Token（事件已记录，不丢）

5. 后台 goroutine 扫描 fallback 表
   ├── 查 status=pending 的记录
   ├── 尝试 Enqueue Asynq
   ├── 成功 → 删除 fallback 记录
   └── 失败 → retryCount++，下次扫描重试

6. Redis 恢复后
   ├── fallback 表中的事件陆续重投成功
   ├── Worker 正常处理
   └── 历史快照延迟落盘（最终一致）
```

---

## 十四、注意事项与局限

| 事项 | 说明 | 应对措施 |
|------|------|---------|
| **集合数量上限** | MongoDB 单库建议 < 1 万个集合 | 类型控制在 500 以内，监控集合数量 |
| **Change Streams 依赖** | 必须运行在副本集模式下 | 已规划 3 节点副本集 |
| **oplog 窗口** | oplog 过小会导致 Resume Token 失效 | 至少 24h 窗口，监控 oplog 大小 |
| **跨类型聚合查询** | 无法在数据库层跨集合查询 | 应用层并发查询后内存聚合，或引入 Elasticsearch |
| **历史集合膨胀** | 全量快照占用空间大 | 永久保留，定期扩容磁盘（详见 §17.2） |
| **Schema 多版本兼容** | 应用层需处理不同版本的数据 | 版本号路由，按版本做差异化处理 |
| **数据迁移耗时** | 大规模历史数据迁移耗时 | 业务低峰期执行，分批处理 |
| **单文档 16MB 限制** | 动态字段滥用可能超限 | 应用层软限 1MB，Schema 限制字段大小 + 数组长度 |
| **字段命名冲突** | 用户定义字段可能与系统保留字段冲突 | Schema 注册时校验保留字段列表 |
| **跨 AZ 延迟** | 跨 AZ 部署时 `w:majority` 延迟 10-30ms | 当前单机部署无此问题，跨 AZ 时预留 50% 余量 |
| **通配符索引局限** | 不支持 `$or`/`$nor` 顶层，复杂查询可能不用 | 高频字段手动建索引，监控慢查询 |
| **versions 数组膨胀** | `schema_definitions` 文档的 versions 数组只增不减，频繁演进可能逼近 16MB 限制 | 版本归档：versions 超 50 条时，将旧版本归档到 `schema_definitions_archive` 集合，`schema_definitions` 仅保留最近 20 版本 + currentVersion 指针 |

---

## 十五、安全性设计

### 15.1 认证

| 组件 | 认证方式 |
|------|---------|
| **MongoDB** | auth + 用户名密码（authSource=admin），按角色分配权限 |
| **Redis** | requirepass（初期单点）；生产主从+哨兵时加哨兵 auth |
| **activelist API** | 用户侧零权限（内网信任，由 zhuzhao 网关鉴权）；服务间 **AK/SK HMAC 验签**（2026-09-03 基线修订，utils `aksk`——本行原「不做认证」口径已被覆盖） |

### 15.2 网络隔离

- activelist 所有进程只监听 Docker 内网 IP
- 仅 zhuzhao 容器可通过 Docker network 访问
- MongoDB / Redis 不对外暴露端口

```
Docker network: activelist_internal
  ├── activelist-apiserver
  ├── activelist-watcher
  ├── activelist-worker
  ├── activelist-mongo-1/2/3
  └── activelist-redis（初期单点，生产可扩展为主从+哨兵）

Docker network: zhuzhao_to_activelist
  ├── zhuzhao
  └── activelist-apiserver（仅暴露 8080 端口）
```

### 15.3 传输加密

- 内网部署，**不做 TLS**（按决策）
- 若未来跨网络部署，需启用 MongoDB TLS + Redis TLS

### 15.4 操作者溯源

**完整链路**：
```
zhuzhao JWT 解析 uid
    ↓
透传 X-Operator: <uid> header
    ↓
activelist apiserver 解析 header
    ↓
写入文档 createdBy / updatedBy 字段
    ↓
Change Stream 捕获事件（含 fullDocument）
    ↓
Worker 从 snapshot.updatedBy / snapshot.createdBy 读取 operator
    ↓
写入历史集合 operator 字段
```

**operator 缺失兜底**：
- `X-Operator` header 缺失时用 `"system"` 兜底
- 数据迁移工具（`cmd/migrate`）默认 operator = `"migrator"`
- Shell 直接操作 MongoDB 时 operator = `"unknown"`

**敏感数据**：
- 调用方（zhuzhao 或业务方）负责敏感数据加密
- activelist 不感知字段敏感性，存储和查询均为明文
- 日志记录时不做字段级脱敏（activelist 不感知字段语义）

---

## 十六、监控告警

### 16.1 监控维度

| 维度 | 指标 | 告警阈值 |
|------|------|---------|
| **MongoDB** | 副本集状态、oplog 窗口大小、连接数、慢查询 | oplog < 1h、从节点延迟 > 10s |
| **Redis** | 进程存活、队列长度、内存使用、连接数 | 队列 > 10000、不可用告警 |
| **Watcher** | 进程存活、token 落后量、事件投递速率、重连次数 | token 落后 > 60s、重连 > 5 次/小时 |
| **Worker** | 任务处理速率、失败率、DLQ 长度、重试次数 | 失败率 > 5%、DLQ > 0 |
| **业务** | 历史缺失率、Schema 变更频率、集合数量、文档大小 | 历史缺失 > 0、集合 > 400 |
| **fallback** | 表长度、重投成功率 | 长度 > 0（说明 Redis 有问题） |

### 16.2 告警渠道

**初期**：
- asynqmon（Asynq Web UI，可视化队列状态、失败任务、DLQ）
- 日志告警（zap logger 输出到 log MongoDB，定时扫描 ERROR/WARN）

**未来扩展（按需引入）**：
- Prometheus + Alertmanager + Grafana（指标告警 + 可视化）

### 16.3 关键告警规则

| 规则 | 触发条件 | 严重级别 |
|------|---------|---------|
| Watcher down | 进程存活探针失败 3 次 | P0 |
| oplog 窗口不足 | oplog < 1h | P0 |
| Redis 不可用 | Redis 连接失败 | P1 |
| DLQ 非空 | DLQ 长度 > 0 | P1 |
| fallback 表积压 | 长度 > 100 | P1 |
| 历史缺失率 | > 0.1% | P2 |
| 集合数量 | > 400 | P2 |
| oplog 过期导致 token 失效 | Resume Token 失效事件发生 | P0 |
| 事件丢失审计 | `event_loss_incidents` 集合有新记录 | P0 |

---

## 十七、容量规划

### 17.1 数据量预估

| 维度 | 估算公式 | 说明 |
|------|---------|------|
| 单集合文档数 | 按业务预期 | 监控并提前分片 |
| 历史集合增长率 | 数据量 × 变更频率 | 每次 Update 写一条历史 |
| oplog 大小 | 至少 24h 窗口 | 按峰值写入速率计算 |
| 磁盘容量 | 业务数据 + 历史数据 × 1.5（冗余） | 预留 50% 余量 |

### 17.2 示例计算

假设（仅作量级参考，实际以业务调研为准）：
- 100 个动态类型
- 每个类型平均 10 万文档
- 每文档平均 1KB（注：应用层软限 1MB，实际多在 KB 级）
- 每文档每天平均变更 5 次
- 历史保留 90 天

| 维度 | 计算 | 结果 |
|------|------|------|
| 业务数据总量 | 100 × 10万 × 1KB | 10 GB |
| 历史数据（年） | 100 × 10万 × 5 × 365 × 1KB | 1.8 TB/年 |
| oplog（24h） | 100 × 10万 × 5 × 1KB | 50 GB |
| 磁盘年增量 | (10 + 1800) × 1.5 | 2.7 TB/年 |

**结论**：历史数据永久保留，磁盘持续增长，需制定扩容规划：
- 年增量约 2.7 TB，按 3 年规划需约 8 TB 磁盘
- 每季度评估磁盘使用率，超过 70% 启动扩容
- 历史快照可用 zstd 压缩（压缩比约 3:1），降低磁盘占用
- MongoDB 分片：单集合超过 500GB 时考虑按 `_id` 范围分片

### 17.3 性能预估

性能指标待上线后压测确定。初步预期：

- 写入瓶颈在 MongoDB 副本集 `w:majority`
- 查询性能取决于索引命中情况
- Change Stream 处理能力取决于 Worker 并发数

> 实际 QPS 取决于文档大小、索引命中、网络延迟等因素，需上线后压测确定。

---

## 十八、部署架构

### 18.1 部署架构图

```
┌─────────────────────────────────────────────────────────────┐
│                    Docker host                                │
│                                                              │
│  ┌─────────────┐                                            │
│  │  zhuzhao    │──┐                                         │
│  └─────────────┘  │                                         │
│                   │ zhuzhao_to_activelist network            │
│                   │                                         │
│  ┌────────────────▼──────────────────────────────────────┐  │
│  │       activelist_internal network                     │  │
│  │                                                       │  │
│  │  ┌──────────────────┐  ┌──────────────────┐           │  │
│  │  │ apiserver × N    │  │ watcher × 1      │           │  │
│  │  │ (无状态)          │  │ (单副本)          │           │  │
│  │  └──────────────────┘  └──────────────────┘           │  │
│  │                                                       │  │
│  │  ┌──────────────────┐  ┌──────────────────┐           │  │
│  │  │ worker × N       │  │ asynqmon × 1     │           │  │
│  │  │ (无状态)          │  │ (监控面板)        │           │  │
│  │  └──────────────────┘  └──────────────────┘           │  │
│  │                                                       │  │
│  │  ┌──────────────────┐  ┌──────────────────┐           │  │
│  │  │ mongo-1/2/3      │  │ redis            │           │  │
│  │  │ (副本集 rs0)      │  │ (初期单点)        │           │  │
│  │  └──────────────────┘  └──────────────────┘           │  │
│  │                                                       │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### 18.2 进程部署说明

| 进程 | 副本数 | 资源建议 | 说明 |
|------|--------|---------|------|
| apiserver | 2+ | 1C 1G / 实例 | 无状态，可横向扩展 |
| watcher | 1 | 0.5C 0.5G | 单副本 + Resume Token 续传（多副本待定） |
| worker | 2+ | 1C 1G / 实例 | 按 Asynq 并发数调整 |
| asynqmon | 1 | 0.1C 0.1G | 监控面板，非必需 |
| mongo | 3 | 2C 4G / 实例 | 按数据量调整 |
| redis | 初期 1，生产 3+ | 1C 2G / 实例 | 初期单点 + fallback 兜底 |

### 18.3 Docker network 设计

| network | 用途 | 可访问者 |
|---------|------|---------|
| `activelist_internal` | activelist 内部组件通信 | 仅 activelist 容器 |
| `zhuzhao_to_activelist` | zhuzhao → activelist apiserver | zhuzhao + apiserver |

**apiserver 同时接入两个 network**，其他 activelist 组件只在 `activelist_internal`。

### 18.4 配置管理

配置文件 `config/config.yaml`，参考 zhuzhao 的 `${VAR:-default}` 环境变量化模式：

```yaml
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 30s

database:
  biz_db:
    url: "mongodb://${BIZ_DB_USERNAME:-coreuser}:${BIZ_DB_PASSWORD:-core_Icsd}@mongo-1:27017,mongo-2:27017,mongo-3:27017/?replicaSet=rs0&retryWrites=true&w=majority"
    db_name: activelist
  log_db:
    url: "mongodb://${LOG_DB_USERNAME:-coreuser}:${LOG_DB_PASSWORD:-core_Icsd}@log-mongo:27017/?replicaSet=rs0"
    db_name: activelist_log

redis:
  addr: "${REDIS_ADDR:-redis:6379}"
  password: "${REDIS_PASSWORD:-core_Icsd}"

asynq:
  concurrency: 10
  retry: 25
  max_deadline_duration: 24h

business:
  history_ttl_days: 0  # 0 表示永久保留，不创建 TTL 索引
  page_size_default: 20
  page_size_max: 100
  doc_size_limit_mb: 1

log:
  level: info  # debug / info / warn / error
```

**配置项说明**：

| 段 | 关键配置 | 说明 |
|----|---------|------|
| `server` | port / timeouts | HTTP 服务配置 |
| `database.biz_db` | MongoDB 连接串 | 业务数据 + 元数据 + Resume Token + fallback |
| `database.log_db` | MongoDB 连接串 | 服务运行日志（zap 输出） |
| `redis` | Redis 地址 | Asynq 队列后端 |
| `asynq` | concurrency / retry | Worker 并发数 + 重试次数 |
| `business` | TTL / 分页 / 文档大小 | 业务参数 |
| `log` | 日志级别 | zap 日志级别 |

---

## 十九、与 zhuzhao 集成

### 19.1 调用关系

```
┌──────────────┐                      ┌──────────────────┐
│   前端/客户端  │                      │  zhuzhao 网关     │
└──────┬───────┘                      │                  │
       │ HTTPS                         │  ┌────────────┐ │
       │                              │  │ 认证鉴权    │ │
       │                              │  │ - JWT 校验  │ │
       │                              │  │ - Casbin    │ │
       │                              │  │ - Restrict  │ │
       │                              │  └────────────┘ │
       │                              │  ┌────────────┐ │
       └─────────────────────────────▶│  │ 反向代理    │ │
                                      │  │ /api/v1/data/*│ │
                                      │  │ /api/v1/admin/types/*│ │
                                      │  └────────────┘ │
                                      └────────┬─────────┘
                                               │ HTTP（内网）
                                               │ Header:
                                               │   X-Operator: <uid>
                                               │   X-Request-ID: <trace_id>
                                               ▼
                                      ┌──────────────────┐
                                      │ activelist       │
                                      │ apiserver        │
                                      │ （内网信任）       │
                                      └──────────────────┘
```

### 19.2 鉴权边界

| 职责 | 归属 | 说明 |
|------|------|------|
| 用户认证（JWT 校验） | zhuzhao | 请求到 zhuzhao 时校验 |
| 接口级鉴权（Casbin） | zhuzhao | `/api/v1/data/*` 注册 Casbin 策略 |
| 资源级鉴权（Restrict） | zhuzhao | 注册单一 `activelist` 资源（全有或全无粒度，不区分 per-type） |
| 操作者透传 | zhuzhao | 透传 `X-Operator: <uid>` header |
| 业务逻辑 | activelist | 动态类型/Schema/CRUD（不做任何权限检查，全依赖上游） |
| activelist 内部审计 | activelist | 自己写 accesslog 到自己的 log 库（详见 §19.7） |
| 网络隔离 | Docker network | activelist 只暴露内网端口 |

**权限粒度决策**：单一 `activelist` 资源（全有或全无）。所有角色对 activelist 的权限是"能访问或不能访问"，不区分 per-type 粒度（如"HR 角色 only 读 user 类型"）。若未来需要 per-type 粒度，需扩展 activelist 类型注册时通知 zhuzhao 动态注册 `activelist:<typeName>` 资源的机制。

### 19.3 Header 透传协议

| Header | 来源 | 用途 |
|--------|------|------|
| `X-Operator` | zhuzhao 从 JWT 提取 `uid` | activelist 写入 `createdBy`/`updatedBy` |
| `X-Request-ID` | zhuzhao 中间件生成 | 跨服务 trace_id 传播，activelist 日志记录 |

### 19.4 zhuzhao 侧改造方案

> **现状**：zhuzhao 当前**没有反向代理模块**（`app/service/proxy/` 不存在），也**没有 `X-Operator` header 透传中间件**（已登记为待办 E13）。以下为需要从零开发的内容。

**新增反向代理模块** `app/service/proxy/`（从零开发，约 3 个文件）：

```
proxy/
├── proxy.go          # 反向代理实现（httputil.ReverseProxy）
├── register.go       # 路由注册 + Resource 资源注册
└── config.go         # 微服务地址配置
```

**配置新增**（`config.yaml` 新增 `microservices` 段）：

```yaml
microservices:
  activelist:
    url: http://activelist-apiserver:8080
    health: http://activelist-apiserver:8080/healthz
    timeout: 30s
  # 未来扩展其他微服务
```

**Restrict 资源注册**（使用 `resourceSvc.Register()`，非已废弃的 `restrictSvc.Registry().Register()`）：

```go
// app/service/proxy/provider.go
func NewProxyService(resourceSvc *resource.ResourceService, ...) *ProxyServiceImpl {
    resourceSvc.Register(&restrictdata.Resource{
        Code:    "activelist",
        Name:    "动态数据平台",
        Actions: []string{"create", "read", "update", "delete", "admin"},
    })
    return &ProxyServiceImpl{...}
}
```

**路由注册**（`register.go`）：

```
/api/v1/data/*       → 代理到 activelist apiserver（Restrict: activelist:read）
/api/v1/admin/types/* → 代理到 activelist apiserver（Restrict: activelist:admin）
```

**中间件链**：
1. `authenticator.Authenticate()` — JWT 校验
2. `authorizor.Authorize()` — Casbin 接口级鉴权
3. `restrict.CheckRestrict("activelist", action)` — 资源级鉴权
4. `SetForwardHeaders()` — 透传 `X-Operator` + `X-Request-Id` header（**需新建**）
5. `proxy.ReverseProxy()` — 反向代理

**新增 `SetForwardHeaders` 中间件**（zhuzhao 待办 E13，约 20 行代码）：

```go
// app/middleware/gin/forward.go
func SetForwardHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        uid, ok := c.Get(domain.ClaimFieldUserID)
        if ok {
            c.Request.Header.Set("X-Operator", uid.(string))
        }
        if traceID, ok := c.Get(trace.HeaderXRequestID); ok {
            c.Request.Header.Set("X-Request-Id", traceID.(string))
        }
        c.Next()
    }
}
```

**accesslog 跳过 body 记录**：

zhuzhao 的 accesslog 中间件对非 GET 请求体做 4KB 截断 + 脱敏记录（`app/service/accesslog/accesslog.go:76-87`）。activelist 的动态字段（手机号/身份证/薪酬等）字段名不命中默认脱敏黑名单（`password/token/secret/private_key`），会明文落盘 zhuzhao 日志库。

**解决方案：proxy 路由跳过 body 记录**

zhuzhao accesslog 中间件增加按路由跳过 body 记录的能力：

```go
// accesslog 配置新增 skip_body_paths
access_log:
  sensitive_fields:
    - "password"
    - "token"
    - "secret"
    - "private_key"
  skip_body_paths:       # 新增：匹配的路径不记录 request_body
    - "/api/v1/data/"
    - "/api/v1/admin/types/"
```

proxy 路由的 accesslog 仅记录 HTTP 元信息（method/path/actor/status_code/cost），不记录 request_body。完整的业务操作审计由 activelist 自己的 accesslog 记录（详见 §19.7 两层审计设计）。

### 19.5 CTEM / SSOT 处理

- **CTEM**：当前不拆分，留在 zhuzhao 内。CTEM 的 `/external/ctem/vuln/new` 是外部系统调用，走 AK/SK 认证，不经过 zhuzhao 网关。未来若拆为独立服务，外部接口可由 ctem-svc 直接暴露或由 zhuzhao 代理。
- **SSOT**：当前半废弃，暂不处理。未来启用时再评估是否拆为独立数据同步服务。

### 19.6 微服务架构演进方向

当前阶段以**功能实现为主**，微服务架构可逐步演进：

- **当前**：activelist 作为第一个独立微服务，zhuzhao 通过反向代理调用，Docker network DNS 提供服务发现，各服务独立配置文件
- **未来**：服务数量增长时，可按需引入服务注册发现（Consul/Nacos）、配置中心、分布式追踪（OpenTelemetry）、集中监控（Prometheus）等

> **演进原则**：按需引入，避免过度设计。当前阶段重点是 activelist 功能实现 + 与 zhuzhao 的集成边界清晰。

### 19.7 审计日志设计

**设计原则：两层审计各司其职**

| 审计层 | 归属 | 记录内容 | 脱敏策略 |
|--------|------|---------|---------|
| **网关层审计** | zhuzhao accesslog | 谁、什么时间、访问了什么 API（method/path/actor/status_code/cost） | proxy 路由跳过 body 记录（避免动态字段明文泄露） |
| **业务层审计** | activelist accesslog | 谁对什么数据做了什么操作（含完整请求体 + 变更前后数据） | activelist 自己控制脱敏（Schema 定义中标记敏感字段） |

**为什么分两层**：
1. **职责清晰**：zhuzhao 负责"认证鉴权审计"（谁通过了鉴权），activelist 负责"业务操作审计"（谁对什么数据做了什么）
2. **脱敏精确**：activelist 知道哪些字段敏感（Schema 定义时可标记），zhuzhao 不知道动态字段语义
3. **审计完整**：activelist 可记录完整业务操作（含变更前/变更后快照），zhuzhao 只能记录 HTTP 请求元信息
4. **日志隔离**：activelist 日志在自己的 log MongoDB，不污染 zhuzhao 日志库

#### 19.7.1 复用 zhuzhao 日志基础设施

activelist 直接复用 zhuzhao 的 `pkg/log/zap/logger` 包（100% 直接复用，无业务耦合）：

| 组件 | 来源 | 复用方式 |
|------|------|---------|
| `pkg/log/zap/logger` | zhuzhao | import 或拷贝 3 文件（zap_logger.go / mongo_writer.go / Config） |
| `pkg/trace` | zhuzhao | import 或拷贝 1 文件（28 行零依赖中性包） |
| `RequestIdMiddleware` | zhuzhao | 拷贝 requestid.go（基于 `gin-contrib/requestid`） |
| `accesslog` 中间件核心 | zhuzhao | 拷贝后替换 uid 提取逻辑（从 `X-Operator` header 提取，而非 JWT claims） |
| `Response` helper | zhuzhao | 拷贝 response.go（500 错误消息脱敏） |

**接入方式**（与 zhuzhao 模式一致）：

```go
// activelist Repository 构造
lc := logClient.Client().Database("activelist").Collection("log")
syncer := logger.NewMongoWriteSyncer(lc, logger.NewConfig(180*24*time.Hour), ctx)  // TTL 180 天
repo.syncer = syncer

// activelist Service Provider 构造
logger := logger.NewLogger(svc.Name(), logger.WithWriter(repo.Syncer()))
svc.log = logger
```

#### 19.7.2 activelist 三进程的日志接入

| 进程 | logger 来源 | 写入集合 | TTL | 记录内容 |
|------|------------|---------|-----|---------|
| **apiserver** | `logger.NewLogger("activelist-apiserver", ...)` | `activelist.log` | 180 天 | HTTP 请求审计 + 业务错误日志 |
| **watcher** | `logger.NewLogger("activelist-watcher", ...)` | `activelist-watcher.log` | 180 天 | Change Stream 事件、Resume Token、重连 |
| **worker** | `logger.NewLogger("activelist-worker", ...)` | `activelist-worker.log` | 180 天 | 任务处理、失败重试、DLQ |

**apiserver accesslog 中间件**（复用 zhuzhao 模式）：
- 中间件链：`Recovery → RequestId → AccessLog`
- accesslog 记录字段：`trace_id` / `actor`（从 `X-Operator` header 提取）/ `action` / `status_code` / `method` / `path` / `query` / `ip` / `cost` / `request_body` / `user_agent`
- `request_body` 4KB 截断 + 敏感字段脱敏（activelist 可在 Schema 定义中标记字段为 `sensitive: true`，accesslog 自动脱敏）
- `action` 注册：`accesslog.RegisterAction("POST", "/api/v1/data/:typeName", "create_data")` 等

**watcher 日志规范**：
- `Info` 级别：stream 启动、重连、Resume Token 保存
- `Warn` 级别：Redis 故障走 fallback、stream 错误重连
- `Error` 级别：Resume Token 失效、oplog 窗口不足

**worker 日志规范**：
- `Info` 级别：任务处理成功（含 taskType / docId / cost）
- `Warn` 级别：历史 Handler 幂等忽略（DuplicateKey）
- `Error` 级别：任务处理失败、重试、进入 DLQ

#### 19.7.3 日志字段规范

**accesslog 审计日志字段**（与 zhuzhao 一致）：

| 字段 | 类型 | 来源 | 说明 |
|------|------|------|------|
| `trace_id` | string | `trace.Field(ctx)` | 跨服务 trace（从 `X-Request-Id` header 继承） |
| `actor` | string | `X-Operator` header | 操作者 uid（zhuzhao 透传） |
| `action` | string | `accesslog.RegisterAction` 注册表 | snake_case，如 `create_data` / `update_schema` |
| `status_code` | int | `c.Writer.Status()` | HTTP 状态码 |
| `method` | string | `c.Request.Method` | HTTP 方法 |
| `path` | string | `c.Request.URL.Path` | 请求路径 |
| `query` | string | `c.Request.URL.RawQuery` | 查询参数 |
| `ip` | string | `c.ClientIP()` | 客户端 IP（zhuzhao 网关 IP） |
| `cost` | string | `time.Since(start)` | 请求耗时 |
| `request_body` | string | 4KB 截断 + 脱敏 | 非 GET 请求体（activelist 控制脱敏） |
| `user_agent` | string | `c.Request.UserAgent()` | User-Agent |
| `timestamp` | time.Time | zap TimeKey | 日志时间（TTL 索引字段） |

**业务错误日志规范**（与 zhuzhao 一致）：
- `Error` 级别：错误时记录，含 `trace.Field(ctx)` + `zap.String("actor", uid)` + `zap.Error(err)`
- `Warn` 级别：警告时记录，同上
- `Info` 级别：Handler/Service 层禁止记录（只在 accesslog 中记录）
- action 用 snake_case（如 `create_data` / `update_schema` / `delete_data`）

#### 19.7.4 敏感字段脱敏策略

activelist 在 Schema 定义中支持 `sensitive: true` 标记：

```json
{
  "fields": {
    "name": { "type": "string", "required": true },
    "phone": { "type": "string", "required": false, "sensitive": true },
    "id_card": { "type": "string", "required": false, "sensitive": true }
  }
}
```

**脱敏实现**：
- apiserver accesslog 中间件在记录 `request_body` 前，查询当前 typeName 的 Schema，将 `sensitive: true` 字段的值替换为 `***`
- 脱敏在 accesslog 层完成，业务逻辑层不感知
- 默认敏感字段黑名单（`password/token/secret/private_key`）仍然生效

---

## 二十、测试策略

### 20.1 单元测试

| 模块 | 测试重点 |
|------|---------|
| Registry | 懒加载创建、幂等性、并发安全 |
| Schema 校验 | 字段类型、必填、保留字段冲突、版本演进兼容 |
| Repository | Insert/Update/Delete/Query、乐观锁冲突、软删除状态机 |
| 历史 Handler | 幂等性（重复 token）、delete 事件处理 |

### 20.2 集成测试

| 场景 | 测试内容 |
|------|---------|
| 端到端流程 | 注册类型 → 插入数据 → 查询 → 更新 → 删除 → 查历史 |
| Schema 演进 | v1 → v2 → v3，旧数据与新数据共存 |
| 事件驱动 | 写入数据 → Change Stream 捕获 → Worker 处理 → 历史落盘 |
| 并发冲突 | 多客户端同时更新同一文档、多管理员同时演进 Schema |

### 20.3 可靠性测试

| 场景 | 测试内容 |
|------|---------|
| Watcher 重连 | 模拟网络抖动、stream 关闭、token 续传 |
| Redis 故障 | 模拟 Redis 不可用、fallback 表写入与重投 |
| Worker 重试 | 模拟历史写入失败、Asynq 重试、DLQ |
| 故障恢复 | 模拟 MongoDB 副本集切换、Redis 重启后的恢复 |

### 20.4 顺序性测试

| 场景 | 测试内容 |
|------|---------|
| 事件乱序 | 同一文档多次 Update 乱序到达 Worker，历史快照各自完整 |
| 历史查询 | 按 `operatedAt` 排序，不依赖写入顺序 |

### 20.5 幂等性测试

| 场景 | 测试内容 |
|------|---------|
| 重复事件 | 同一 Resume Token 重复投递，历史集合不产生重复记录 |
| fallback 重投 | fallback 表中的事件重投成功后，历史不产生重复 |

---

## 二十一、未来扩展方向

| 方向 | 说明 |
|------|------|
| **跨类型聚合搜索** | 引入 Elasticsearch 作为搜索索引 |
| **复杂工作流编排** | 从 Asynq 升级到 Temporal，支持 Saga 事务 |
| **消息队列升级** | 当 Redis 队列成为瓶颈时，迁移到 Kafka |
| **数据分级存储** | 冷数据自动归档到 OSS，降低存储成本 |
| **Schema 可视化** | 提供 Schema 变更审批流程和可视化对比工具 |
| **Watcher 多副本** | 单副本重启延迟无法接受时，引入选主机制 |
| **Redis 高可用** | 初期单点验证后，升级为主从 + 哨兵 |
| **跨 AZ 部署** | 云部署场景，`w:majority` 跨 AZ 延迟 10-30ms，容量规划预留 50% 余量 |
| **微服务基础设施** | 服务发现 / 配置中心 / 分布式追踪 / 集中监控 |

---

## 二十二、总结

本方案通过 **多集合物理隔离 + 动态 Schema 校验 + Change Streams + Asynq + 全量快照历史 + 版本化 Schema** 六层架构，构建了一个：

- ✅ **无需重启**即可扩展新类型和字段的系统
- ✅ **写入质量可控**（动态校验拦截脏数据）
- ✅ **查询性能高效**（物理隔离 + 精准索引）
- ✅ **事件处理可靠**（Change Streams + Asynq 持久化队列 + Resume Token + fallback 兜底；**oplog 窗口内**事件不丢失，详见 §7.2/§12）
- ✅ **数据全生命周期可追溯**（历史集合永久保留，记录每一次变更，不可变审计日志）
- ✅ **Schema 全生命周期可管理**（版本化 Schema，向前兼容演进，版本不可删除）
- ✅ **基础设施高可用**（MongoDB 3 节点副本集 + Redis 初期单点 + fallback 兜底）
- ✅ **并发安全**（乐观锁防止冲突 + Schema 原子变更 + 建议串行执行）
- ✅ **内网信任部署**（由 zhuzhao 网关统一鉴权，activelist 不处理认证）
- ✅ **两层审计日志**（zhuzhao 网关层元信息审计 + activelist 业务层完整操作审计，复用 zhuzhao zap 日志基础设施）

是一个适合**数据类型持续增长、字段频繁变动、需要审计溯源和高可靠性事件处理**的业务场景的成熟技术方案。

### 关键决策汇总

| 决策项 | 选择 |
|--------|------|
| Asynq 任务处理模式 | 中间件模式（历史 Handler → 业务 Handler） |
| Update Schema 校验 | 按数据原 `schemaVersion` + 最新版本分类校验（方案 B，待业务方确认） |
| Update 改新版字段 | optional 字段允许（schemaVersion 不变）；required 字段拒绝 422（需数据迁移） |
| schemaVersion 语义 | 创建时版本，Update 不改变（方案 B 推荐，对标 Confluent Schema Registry；待业务方确认） |
| Schema 缓存 | apiserver 内存缓存，TTL 60s 过期重新加载 |
| Schema 并发演进 | FindOneAndUpdate 原子操作，后到者全量定义新版本（非字段级 merge），建议串行执行 |
| 鉴权 | zhuzhao 网关统一鉴权，activelist 内网信任，不做任何权限检查 |
| 权限粒度 | 单一 `activelist` 资源（全有或全无），不区分 per-type |
| 基础设施 | 完全独立部署（Mongo 3 节点 + Redis 初期单点） |
| Watcher 部署 | 初期单副本，正式环境多副本 + 选主（详见 §7.5） |
| Resume Token 存储 | Mongo `resume_tokens` 集合 |
| Worker 幂等 | 历史 Handler：`{resumeTokenHash: 1}` 唯一索引；业务 Handler：必须自行实现幂等 |
| Redis 故障兜底 | Mongo `event_fallback` 表 + 后台重投 |
| 软删除语义 | 严格模式（删除后拒绝写） |
| 跨集合事务 | 不使用（单文档原子 + 异步历史 + 最终一致） |
| 安全方案 | 仅认证（Mongo / Redis auth），无 TLS |
| 类型删除 | 禁止删除，只支持 deprecated |
| 查询安全 | 字段白名单 + 操作符黑名单 + 强制分页 |
| API 版本号 | `/api/v1/` |
| 响应格式 | 统一 `{code, msg, data}` 包装，与 zhuzhao 网关格式一致 |
| 历史集合保留 | 永久保留，不设 TTL，磁盘定期扩容（详见 §17.2） |
| 审计日志 | 两层审计：zhuzhao 网关层（跳过 body）+ activelist 业务层（完整记录 + 敏感字段脱敏） |
| 日志基础设施 | 复用 zhuzhao `pkg/log/zap/logger` + `pkg/trace` + accesslog 中间件模式 |
| 敏感数据 | activelist Schema 标记 `sensitive: true` 字段，accesslog 自动脱敏 |
| 事件可靠性承诺 | oplog 窗口内事件不丢失（非绝对承诺）；oplog 过期后事件丢失不可恢复，靠监控+扩容预防 |
| 迁移与 Schema 变更 | 互斥（迁移期间禁止 Schema 变更，心跳 30s + TTL 5min 自动释放僵尸锁，详见 §6.6） |
| 迁移事件队列 | 独立 Asynq 队列 `activelist-migrate`，避免阻塞业务事件 |
| versions 数组上限 | 超 50 条时归档到 `schema_definitions_archive`，仅保留最近 20 版本 |

---

## 二十三、待确认决策清单（架构师评审汇总）

> 本章节为架构师评审后汇总的待确认决策项，**尚未在正文中实施**。所有项需业务方/技术方逐一确认后，再统一执行修订。
>
> **背景**：用户核心需求是"存储自定义键值对类型的活动列表 + CRUD，后续很多服务都要用，所有数据（含历史审计）一定不能丢；字段类型基本只 int/string，字段数量会经常新增"。
>
> 每项均附**业界最佳实践参考**与**适用场景**，辅助决策。

### 23.1 核心方案方向（5 项，最高优先级）

| 编号 | 决策项 | 推荐方案 | 业界参考 | 影响范围 | 待确认 |
|------|--------|---------|---------|---------|--------|
| **F1** | 采用方案 F（同步事务写 + 单版本 Schema） | ✅ 推荐 | Transactional Outbox Pattern（Microservices.io）；MongoDB Multi-Document Transactions（4.0+ 官方）；Audit Log in Same Transaction（Martin Fowler） | 全文重构 | ☐ 同意 / ☐ 调整 |
| **F2** | 砍掉 watcher/worker/Asynq/Redis（队列）/Change Stream | ✅ 推荐 | KISS 原则；YAGNI 原则；"Worse is Better"（Richard Gabriel） | §1/§2/§7/§8/§12/§18 等 | ☐ 同意 / ☐ 保留部分 |
| **F3** | 主数据 + 历史同事务写入（保证绝对不丢） | ✅ 推荐 | Same-DB Transaction（同库强一致）；DynamoDB Transactions（AWS）；RDBMS Audit Log Pattern | §7/§10/§12 | ☐ 同意 / ☐ 改异步 |
| **F4** | Schema 采用方案 D（单版本 + 宽松演进，删除 schemaVersion 字段） | ✅ 推荐 | Confluent Schema Registry `BACKWARD` 兼容模式；MongoDB Native Schema Validation（`$jsonSchema` validator，latest-only）；Avro Writer Schema 模式 | §5/§6/§10/§11 | ☐ 同意 / ☐ 保留多版本 |
| **F5** | operatedAt 改为与主数据 updatedAt 同源时间戳 | ✅ 必修 | Debezium CDC（用 oplog `clusterTime`）；Kafka Streams Event-Time 语义；Watermark 乱序处理 | §5.3/§7.3 | ☐ 同意 |

**方案 F 说明**：
- 架构：3 进程 → 1 进程（仅 apiserver）
- 存储：MongoDB + Redis（可选）→ MongoDB（Redis 可选）
- 历史：Change Stream 异步 → **事务内同步写**（commit 即主数据 + 历史都持久化，绝对不丢）
- Schema：多版本共存 → **单版本运行时校验 + 宽松演进**（新增 optional 字段无需迁移，旧数据立即可读写；破坏性变更拒绝 422）
- 代价：写延迟增加约 30-50%（多一次历史写入 + 事务开销）；删除事件订阅能力

**方案 F 适用场景**：
- ✅ 审计数据要求"绝对不丢"（金融、合规、医疗）
- ✅ 核心数据 CRUD 场景，写入频率中等（< 5000 QPS）
- ✅ 希望简化架构，避免引入消息队列/事件流基础设施
- ✅ 字段类型基本固定，主要新增 optional 字段
- ❌ 不适合：超高写入吞吐（事务开销成瓶颈）；需要复杂事件编排（Saga/工作流）

---

### 23.2 开放问题（6 项）

| 编号 | 问题 | 选项 | 业界参考 | 推荐 | 待确认 |
|------|------|------|---------|------|--------|
| **Q1** | 业务事件订阅能力 | A. 不提供（MVP）<br>B. webhook（事务后异步通知）<br>C. 保留 Asynq 仅用于业务事件 | Webhook Pattern（Stripe/GitHub，HMAC 签名+重试+幂等）；Transactional Outbox + Relay（Debezium）；Server-Sent Events (SSE) | A | ☐ A / ☐ B / ☐ C |
| **Q2** | Redis 是否保留 | A. MVP 不引入（限流内存 + 缓存进程内）<br>B. 引入（跨实例共享限流 + 缓存失效广播） | 进程内限流（`golang.org/x/time/rate`，单实例 token bucket）；Redis Cell（分布式限流）；一致性哈希 + 本地缓存 | A | ☐ A / ☐ B |
| **Q3** | 批量操作 API 上限 | 建议 100 条/请求，超过返回 400 | GitHub API（100/页）；Stripe API（100/页）；MongoDB `insertMany`（≤1000 防单事务过大）；DynamoDB BatchWriteItem（25/请求） | 100 | ☐ 100 / ☐ 其他 |
| **Q4** | 限流阈值与策略 | 全局 1000 QPS + per-route 100 QPS；是否加 per-client？ | Token Bucket 算法（RFC）；Stripe（per-user tier）；GitHub（5000/h per-token）；Sentinel（Alibaba） | 不加 per-client | ☐ 全局+route / ☐ 加 per-client |
| **Q5** | `cmd/types delete` 工具 | A. 提供（删前强制检查集合为空）<br>B. 不提供（保持"类型不可删除"） | Soft Delete + Grace Period（DynamoDB TTL；Salesforce recycle bin）；Hard Delete with Pre-check（PostgreSQL `DROP TABLE` 前 `COUNT=0`）；Kubernetes Finalizers | A | ☐ A / ☐ B |
| **Q6** | 事务失败重试策略 | A. 事务失败直接返回 503，客户端重试<br>B. 应用层重试 3 次 | MongoDB `retryWrites=true`（自动重试 TransientTransactionError）；`WriteConflict` 重试；Spring `@Transactional` RetryTemplate（3 次指数退避） | A | ☐ A / ☐ B |

**Q1 方案对比与适用场景**：

| 选项 | 适用场景 | 代价 |
|------|---------|------|
| A. 不提供 | MVP 阶段，下游无明确订阅需求；下游可轮询 history | 下游实时性差 |
| B. Webhook | 少量下游（< 10 个），可接受偶发丢失（异步通知失败仅告警） | 需实现签名/重试/幂等；下游需暴露 endpoint |
| C. 保留 Asynq | 多下游（> 10 个），需可靠投递 + DLQ + 重试 | 重新引入 Redis + Worker，复杂度回升 |

**Q2 方案对比与适用场景**：

| 选项 | 适用场景 | 代价 |
|------|---------|------|
| A. MVP 不引入 | 单实例或少量实例（≤3）；限流配额无需全局精确 | 多实例时限流配额翻倍（N×100 QPS） |
| B. 引入 Redis | 多实例（>3）需全局精确限流；Schema 缓存需跨实例失效广播 | 多一个中间件依赖，运维复杂度+1 |

**Q6 方案对比与适用场景**：

| 选项 | 适用场景 | 代价 |
|------|---------|------|
| A. 直接 503 | 客户端有重试能力（如 zhuzhao proxy）；避免双层重试混乱 | 客户端体验稍差 |
| B. 应用层重试 3 次 | 客户端无重试能力；希望服务端自愈 | 双层重试风险（MongoDB 自动 + 应用层） |

---

### 23.3 数据模型补充（4 项，P1）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **D1** | 嵌套字段/数组字段 | 实际业务可能有 `tags: ["a","b"]` 或 `address: {city:"..."}`。Schema 如何校验嵌套？`changedFields` 如何表达 `address.city` 变更？ | JSON Schema draft-07（`properties` 嵌套 + `items` 数组 + `additionalProperties:false`）；MongoDB Dot Notation（`$set:{"address.city":"..."}`）；gojsonschema 完整支持 draft-07 | P1 | ☐ |
| **D2** | 字段默认值 | 新增 optional 字段时旧数据无该字段。Schema 是否支持 `default`？查询时填充还是返回 missing？ | JSON Schema `default` 关键字（仅文档化，不自动填充）；MongoDB `$setOnInsert`（仅 insert 填充）；PostgreSQL `DEFAULT`（DDL 级） | P2 | ☐ |
| **D3** | 字段唯一性约束 | 某些字段需唯一（如 `user.code`）。通配符索引不保证唯一。动态 Schema 下如何支持 per-field 唯一？ | MongoDB Unique Index；Partial Unique Index（`partialFilterExpression:{status:"active"}`，软删除数据不参与唯一性）；PostgreSQL Partial Index | P1 | ☐ |
| **D4** | 动态索引管理 API | 新增高频查询字段需手动建索引。是否需要 `POST /api/v1/admin/types/:typeName/indexes` API？ | MongoDB Atlas（REST API 管理索引）；Elasticsearch PUT mapping（动态添加字段映射）；Kubernetes API（声明式资源管理） | P1 | ☐ |

**D1 适用场景**：
- 平铺字段（`name`/`age`）：简单业务，推荐 MVP
- 嵌套对象（`address: {city, street}`）：结构化数据，需支持 dot-notation 更新
- 数组字段（`tags: ["a","b"]`）：标签类，用 `$push`/`$pull` 操作

**D2 方案对比与适用场景**：

| 方案 | 适用场景 | 代价 |
|------|---------|------|
| 不支持 default | 业务方自行处理 missing 值 | 下游需处理 nil |
| 查询时填充 | 旧数据无新字段，查询时补默认值 | 查询逻辑复杂 |
| Insert 时 $setOnInsert | 新数据有默认值，旧数据无 | 历史数据需迁移补齐 |

**D3 适用场景**：
- 普通唯一（全局）：用户名/邮箱唯一
- Partial 唯一（仅 active）：软删除后可重新创建同名数据

---

### 23.4 查询能力补充（5 项，P0-P1，**当前方案明显缺口**）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **Q5-1** | 排序支持 | §6.7 只讲分页，**没讲排序**。`?sort_by=name&sort_order=asc`？排序字段白名单？ | GitHub API（`?sort=created&direction=desc`）；JSON:API Spec（`?sort=-name,age`，- 降序 + 升序）；Stripe API（`?sort=created`） | **P0** | ☐ |
| **Q5-2** | 字段投影 | 查询时只返回部分字段 `?fields=name,age`？大文档减少网络传输 | JSON:API Sparse Fieldsets；GraphQL Field Selection；MongoDB Projection（`{name:1, age:1, _id:0}`） | P1 | ☐ |
| **Q5-3** | 游标分页（深分页） | 当前 offset 分页，翻到第 1000 页性能极差。是否提供 cursor 分页？ | Stripe Cursor Pagination（`?starting_after=xxx`）；GraphQL Relay Cursor Connection Spec；MongoDB `_id` Range（`{_id:{$gt:lastId}}`） | **P0** | ☐ |
| **Q5-4** | 聚合查询 | `count by status`、`sum by group`？当前只有 list + count，无聚合。 | MongoDB Aggregation Pipeline（`$match`→`$group`→`$sum`）；Elasticsearch Aggregations；PostgreSQL GROUP BY | P2 | ☐ |
| **Q5-5** | 全文搜索 | string 字段模糊搜索用 `$regex` 性能差。是否需要 MongoDB text index？还是引入 ES？ | MongoDB Atlas Search（Lucene，支持中文分词）；Elasticsearch（全文搜索标准）；PostgreSQL Full-Text Search（`tsvector`） | P2 | ☐ |

> 当前 §6.7 查询设计**过于单薄**，只有 filter + 分页，缺排序/投影/游标分页。对"很多服务都要用"的核心服务明显不足。

**Q5-3 方案对比与适用场景**：

| 方案 | 适用场景 | 代价 |
|------|---------|------|
| Offset 分页 | 前几页，总页数 < 100 | 深分页性能差（skip 全表扫描） |
| Cursor 分页 | 深分页、无限滚动、大数据量 | 不支持跳页；排序字段必须稳定唯一 |

**Q5-5 方案对比与适用场景**：

| 方案 | 适用场景 | 代价 |
|------|---------|------|
| `$regex` 模糊 | 少量数据、简单前缀匹配 | 性能差，全表扫描 |
| MongoDB Text Index | 中等数据量、单字段搜索 | 不支持中文分词（需第三方） |
| Atlas Search / ES | 大数据量、中文全文搜索 | 引入额外组件，运维复杂 |

---

### 23.5 事务与一致性（3 项，P0，**方案 F 落地关键**）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **T1** | 事务超时 | MongoDB 事务默认 60s 超时。历史集合写入慢或锁等待时事务超时怎么办？ | MongoDB `transactionLifetimeLimitSeconds`（默认 60s，可调）；Spring `@Transactional(timeout=30)`；最佳实践：事务内操作 ≤1s | **P0** | ☐ |
| **T2** | 事务隔离级别与并发 | 方案 F 在事务内"读文档→校验→更新"。并发事务都读到 version=1，commit 时如何处理？ | MongoDB Snapshot Isolation；OCC（乐观并发控制）+ `WriteConflict` 自动重试；PostgreSQL Repeatable Read | **P0** | ☐ |
| **T3** | 批量写入的事务边界 | 批量插入 100 条，一个事务还是每条独立事务？第 50 条失败：全部回滚还是 49 成功+1 失败？ | MongoDB `insertMany(ordered=true)`（遇错停止）；`insertMany(ordered=false)`（继续插入，返回每条错误）；PostgreSQL `SAVEPOINT` | **P0** | ☐ |

**T2 适用场景**：
- 方案 F 下：filter `{_id, version, status:"active"}` + 事务 = OCC，MongoDB 自动处理 `WriteConflict`
- 高冲突场景（同一文档高频更新）：考虑悲观锁（`findAndModify` 占用文档锁）

**T3 方案对比与适用场景**：

| 方案 | 适用场景 | 代价 |
|------|---------|------|
| All-or-nothing（事务 + ordered） | 批量必须全部成功或全部失败 | 单条失败拖累整批 |
| Best-effort（unordered + 错误数组） | 容忍部分失败，返回成功/失败列表 | 调用方需处理部分失败 |

---

### 23.6 安全（3 项，P1-P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **S1** | $regex ReDoS | 操作符黑名单禁了 `$where`，但允许 `$regex`。恶意正则可导致 ReDoS。是否限制正则复杂度或超时？ | Google RE2（Go 原生 `regexp` 包，线性时间无回溯，天然防 ReDoS）；OWASP Regex DoS Prevention（限制长度、禁嵌套量词） | P1 | ☐ |
| **S2** | 数据导出审计 | 批量查询/导出大量数据时是否审计？防止数据爬取（如一次查 10 万条） | SOC2 Audit Logging；SIEM Systems（Splunk/ELK）；GDPR Art. 30（记录数据访问日志） | P2 | ☐ |
| **S3** | 租户隔离预留 | 当前单租户。未来多租户时 Schema 是否预留 `tenantId`？现在不加后期改造代价大 | PostgreSQL Row-Level Security (RLS)；Salesforce Multi-Tenancy（每条数据带 OrganizationId）；MongoDB 复合索引 `{tenantId:1, ...}` | P2 | ☐ |

**S1 适用场景**：Go 已用 RE2，ReDoS 风险低；仍建议限制正则长度 ≤100 字符 + 超时控制（context with deadline）

**S2 适用场景**：
- 合规要求（SOC2/GDPR）：必须审计
- 内部安全：大型导出（> 1000 条）记录告警

**S3 适用场景**：
- 单租户 MVP：不预留，后期改造代价大（所有查询+索引都要改）
- 多租户预留：所有数据加 `tenantId` 字段 + 复合索引，当前单租户用固定值

---

### 23.7 性能（3 项，P1-P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **P1** | 历史集合写入瓶颈 | 方案 F 每次 Update 同事务写一条历史，高写入场景 history 集合成瓶颈。是否需要批量写或分片？ | Event Sourcing Snapshot Pattern（Martin Fowler，每 N 次变更合并）；MongoDB Sharding（按 `typeName` 或 `docId` 分片）；MongoDB Time-Series Collection（5.0+，按时间自动分桶） | P1 | ☐ |
| **P2** | Schema 缓存内存占用 | 类型多了后所有 Schema 常驻内存。是否需要 LRU 淘汰？上限多少？ | LRU Cache（`golang.org/x/exp/lru`）；Redis maxmemory-policy（`allkeys-lru`）；Guava Cache（Java，`maximumSize`+`expireAfterWrite`） | P2 | ☐ |
| **P3** | 热点集合 | 高频类型（如 col_user）可能成热点。单 DB 多集合是否需要分库？ | MongoDB Sharding（`sh.shardCollection()` 按 `_id` hashed）；Vertical Sharding（按 typeName 分库）；Read Replica（读流量分流到从节点） | P2 | ☐ |

**P1 适用场景**：
- 中低写入（< 1000 QPS）：单集合足够
- 高写入：分片 or snapshot 合并

**P2 适用场景**：
- 类型 < 500：全量常驻内存，无需 LRU
- 类型 > 500：LRU 上限 500，TTL 60s

**P3 适用场景**：
- 单集合 < 500GB：单库足够
- 单集合 > 500GB 或 QPS > 10000：分片

---

### 23.8 API 设计（3 项，P1-P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **A1** | Insert 幂等性 | 客户端重试导致重复插入？是否支持 `Idempotency-Key` header？ | Stripe Idempotency-Key（header 带 key，24h 内同 key 返回相同结果）；RFC draft-ietf-httpapi-idempotency-key-header；MongoDB 唯一索引兜底 | P1 | ☐ |
| **A2** | API 版本兼容 | `/api/v1/` 未来 v2 如何兼容旧客户端？新旧版本共存策略？ | Stripe API Versioning（URI 版本化，旧版本长期支持）；GitHub API（header 版本化）；Semantic Versioning（major 破坏性，minor 向后兼容） | P2 | ☐ |
| **A3** | 变更通知替代 | 方案 F 砍了事件驱动，下游想"数据变更通知我"。是否提供 Webhook/轮询？ | Stripe/GitHub Webhooks（HMAC 签名+重试）；Server-Sent Events (SSE)；Long Polling | P1 | ☐ |

**A1 适用场景**：
- 客户端重试场景（网络抖动）：必须支持
- 幂等 key 存储：Redis 24h TTL 或 MongoDB 集合

**A2 适用场景**：
- MVP：仅 `/v1/`
- 未来 v2：URI 版本化 + 旧版本 deprecation 期 ≥6 个月

**A3 方案对比与适用场景**：

| 方案 | 适用场景 | 代价 |
|------|---------|------|
| Webhook | 少量下游、可接受偶发丢失 | 实现签名/重试/幂等 |
| SSE | 实时性要求高、少量下游 | 长连接占用资源 |
| 轮询 history | 简单、无状态 | 实时性差（秒级延迟） |

---

### 23.9 Schema 管理（3 项，P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **SC1** | Schema 回滚 | 演进搞错了能否回退？方案 D"版本不可删除"，但能否切换 `currentFields` 指向旧版本？ | Confluent Schema Registry Rollout（切换指针到历史版本）；Flyway Undo（已废弃，推荐 forward-only + rollback migration）；Git Revert（前向修复） | P2 | ☐ |
| **SC2** | Schema 导入导出 | dev→test→prod 的 Schema 迁移？跨环境同步 | Schema as Code（YAML + Git 版本控制）；Terraform（声明式基础设施）；Liquibase / Flyway（数据库 schema 版本控制） | P2 | ☐ |
| **SC3** | Schema 审批流程 | 变更直接生效还是需审批？多管理员协作场景 | GitOps PR Review（Schema 变更通过 PR 审批）；Confluent Compatibility Checker（自动校验 BACKWARD 兼容性）；ArgoCD / Flux（GitOps 部署） | P2 | ☐ |

**SC1 适用场景**：
- 方案 D：切换 `currentFields` 指针到旧版本（不删版本）
- 破坏性变更误操作：需数据迁移修复，无法简单回滚

**SC2 适用场景**：
- dev→test→prod：导出 YAML，Git 管理，环境间同步
- `cmd/schema export --type=X > X.yaml` / `cmd/schema import --file=X.yaml`

**SC3 适用场景**：
- 小团队：直接生效 + 操作日志审计
- 大团队：PR 审批 + 兼容性自动校验

---

### 23.10 数据生命周期（3 项，P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **L1** | 历史数据归档 | history 永久保留但冷数据可归档到 cheaper storage（如 1 年前移到归档集合/OSS） | MongoDB Cold Tier（Atlas，热数据 SSD + 冷数据 S3）；AWS S3 Glacier（极低存储成本，恢复需小时级）；InfluxDB Retention Policy（按时间自动降采样+归档） | P2 | ☐ |
| **L2** | GDPR/被遗忘权 | 软删除永久保留，但某些场景要求物理删除。是否支持？历史快照如何处理？ | GDPR Art. 17 Right to Erasure（物理删除）；Apache Unomi（匿名化处理）；Kafka Log Compaction（保留 key 删除 value） | P2 | ☐ |
| **L3** | per-type TTL | 不同类型数据保留时长不同？config 里 per-type 配置？ | MongoDB TTL Index（`createIndex({createdAt:1}, {expireAfterSeconds:...})`）；DynamoDB TTL（per-item 过期时间）；Cassandra TTL（per-cell 过期） | P2 | ☐ |

**L1 适用场景**：
- 1 年内热数据：MongoDB
- 1 年前冷数据：归档集合（压缩）或 OSS

**L2 适用场景**：
- 主数据：物理删除（或 anonymization 替换敏感字段为 hash）
- 历史快照：anonymization（替换敏感字段，保留审计骨架）

**L3 适用场景**：
- 不同类型数据保留时长不同：config 里 per-type `ttl_days`
- 审计数据：`ttl_days: 0`（永久保留）

---

### 23.11 运维与交付（4 项，P1-P2）

| 编号 | 点 | 说明 | 业界参考 | 优先级 | 待确认 |
|------|----|------|---------|--------|--------|
| **O1** | 灾难恢复/PITR | Mongo 集群整体挂了怎么办？异地备份？PITR 恢复？RPO/RTO 目标？ | MongoDB Continuous Backup（Atlas，oplog 留存 24h+，PITR 到任意秒）；AWS RDS PITR（5 分钟 RPO）；pgBackRest（PostgreSQL PITR） | P1 | ☐ |
| **O2** | 版本升级/灰度 | apiserver 多副本升级时新旧版本共存，Schema 缓存格式不兼容怎么办？ | Kubernetes Canary Deployment（10%→50%→100%）；Argo Rollouts（自动化灰度）；Schema 缓存版本号兼容检查 | P2 | ☐ |
| **O3** | 数据一致性校验工具 | 定期校验 col_X 文档数 vs col_X_history 的 insert 事件数（方案 F 下应完全一致）。`cmd/consistency-check` | Lambda Reconciliation（AWS，定时对账任务）；Debezium QoS（事件投递质量监控）；Financial Reconciliation（金融业对账标准） | P1 | ☐ |
| **O4** | API 文档自动生成 | Swagger/OpenAPI？动态类型的 API 文档如何生成？ | OpenAPI 3.0（Swagger UI / ReDoc）；动态类型用 generic schema + `additionalProperties`；Postman Collection（自动生成测试集合） | P2 | ☐ |

**O1 适用场景**：
- RPO ≤5min / RTO ≤1h：MongoDB oplog 备份 + 每日全量
- 更高要求：异地灾备 + 跨 region 副本

**O2 适用场景**：
- 兼容升级：直接滚动更新
- 破坏性升级：蓝绿部署或灰度

**O3 适用场景**：
- 方案 F 下：col_X 文档数 == col_X_history 的 insert 事件数（应完全一致）
- 定时对账（每日凌晨）+ 告警差异

**O4 适用场景**：
- 动态类型：API 文档描述通用接口，字段定义单独文档
- 静态部分（admin/types）：可完整 OpenAPI

---

### 23.12 已覆盖点（确认无遗漏，无需再决策）

以下点已在正文或前序评审中覆盖，附业界参考：

| 已覆盖点 | 业界参考 |
|---------|---------|
| ✅ 健康检查 `/healthz` `/readyz` | Kubernetes Probes Spec（liveness/readiness/startup 三探针） |
| ✅ 优雅启动/停止 | Kubernetes Graceful Shutdown（`terminationGracePeriodSeconds` + preStop hook） |
| ✅ 限流 | Token Bucket（RFC）；Sentinel（Alibaba） |
| ✅ 连接池/超时配置 | HikariCP（Java 最佳实践）；MongoDB `maxPoolSize=100` |
| ✅ 监控指标 + `/metrics` | Prometheus + Grafana；USE Method（Utilization/Saturation/Errors） |
| ✅ trace_id 跨服务传播 | OpenTelemetry；W3C Trace Context |
| ✅ 批量操作 API | GraphQL Batch；REST Batch Endpoint |
| ✅ 计数 API | RESTful count endpoint（`GET /count?filter=...`） |
| ✅ 数据恢复 API | Soft-delete restore（Salesforce recycle bin） |
| ✅ 字段重命名（方案 D 用"加新+废弃旧"替代） | Confluent Schema Registry 字段废弃模式 |
| ✅ 数据导出导入工具 | `mongodump`/`mongorestore`；`pg_dump`/`pg_restore` |
| ✅ 类型删除工具 | Kubernetes Finalizers（删除前执行清理钩子） |
| ✅ zhuzhao proxy 失败处理/熔断 | Circuit Breaker Pattern（Martin Fowler）；`sony/gobreaker` |
| ✅ 两层审计 | Defense in Depth（多层防御）；SOC2 审计分层 |
| ✅ 敏感字段脱敏 | OWASP Sensitive Data Exposure；PCI DSS Masking |
| ✅ Schema 缓存 singleflight（防击穿） | `golang.org/x/sync/singleflight`；Cache Stampede 防护 |
| ✅ Schema 缓存多副本主动失效 | Cache Invalidation via Change Stream；Redis Pub/Sub 广播 |
| ✅ operatedAt 审计正确性（方案 F 同源时间戳） | Event-Time vs Processing-Time（Kafka Streams 语义） |

---

### 23.13 修订执行计划（待 F1-F5 确认后启动）

**第一阶段：A 组（架构简化）+ B 组（Schema 方案 D）**
- A1-A14：砍掉 watcher/worker/Asynq/Change Stream；重画架构图；删除 §7/§13 流程示例等
- B1-B12：删除 schemaVersion 字段；§5.4 整节重写为单版本 + 宽松演进；§6.6 简化迁移工具；§10.4 反转为"使用事务"

**第二阶段：C 组（可靠性/可用性补全）**
- C1-C2：Schema 缓存 singleflight + 多副本主动失效
- C3-C5：批量操作 + 计数 + 恢复 API
- C6-C8：连接池配置 + 健康检查 + 优雅启动
- C9-C11：限流 + 监控指标 + /metrics 端点

**第三阶段：D 组（zhuzhao 集成补全）**
- D1：proxy 失败处理（502/超时/熔断）
- D2：资源注册启动解耦
- D3：zhuzhao accesslog 记录下游代理信息

**第四阶段：23.3-23.11 待确认项落地**
- 待 23.1-23.11 各项决策完成后，按优先级逐项落地到正文

---

### 23.14 影响预估

| 维度 | 当前 | 方案 F 修订后 | 变化 |
|------|------|--------------|------|
| 文档行数 | 2037 | 预计 1400-1600 + 本清单 | -400 到 -600（大幅简化） |
| 进程数 | 3（apiserver+watcher+worker） | 1（apiserver） | -2 |
| 存储依赖 | MongoDB + Redis | MongoDB（Redis 可选） | -1 |
| 核心复杂度 | V3（事件驱动 + Schema 多版本） | V1（同步事务 + 单版本 Schema） | 大幅降低 |
| 历史可靠性 | 尽力而为（oplog 窗口内） | **绝对不丢**（事务原子） | 升级 |
| 审计正确性 | operatedAt 顺序 bug | 完全正确（同源时间戳） | 修复 |

---

## 二十四、与 zhuzhao 集成形态决策（已定，ADR-003）

> 本节为已采纳的架构决策，对应 `docs/adr/ADR-003-activelist-integration-form.md`。下文"待确认"项仅指落地细节，集成形态本身已锁定。

### 24.1 决策结论

| 维度 | 决策 | 说明 |
|------|------|------|
| 集成形态 | **独立部署进程（C2'）：代码同仓库 `internal/activelist/`，独立二进制 + 独立容器；zhuzhao 仅经反代 HTTP 调用，不 import 进请求路径** | 保留部署边界 = 故障隔离；复用 zhuzhao 基础设施（pgx/JSONB/Outbox/`pkg/log`）；将来可机械拆为独立仓库 |
| 数据库 | **Mongo → PostgreSQL（统一技术栈）** | 与 zhuzhao 同源（JSONB/ltree/Outbox/Casbin-pgx）；PG 事务能力显著强于 Mongo |
| 耦合方式 | **事件总线对接（非事务耦合）** | activelist 变更事件落 zhuzhao 的 **L1 `ticket_events`（事件源，ADR-001）**，由 L1 消费者/Asynq worker 分发，工单/其他模块订阅；Asynq 仅执行器，不当总线 |
| 鉴权边界 | 不变（§19.2） | zhuzhao 统一 JWT/Casbin/Restrict，内网透传 `X-Operator` |
| 网络隔离 | 不变（§18.2） | apiserver 跨双 network，仅 zhuzhao 可达 |

### 24.2 与方案 F 的关系（重要）

§23.1 的**方案 F（同步事务写 + 主数据/历史同事务 + 砍掉 watcher/worker/Change Stream）** 与本节决策**高度契合、互相强化**：
- 方案 F 把"历史快照"从异步 Change Stream 改为**事务内同步写**，正是 PG 的强项（完整 ACID 事务，无 Mongo 多文档事务的 16MB/60s/副本集限制）。转 PG 后方案 F 的"绝对不丢"更易实现。
- 砍掉 watcher/worker/Asynq/Redis 后，activelist 只剩 apiserver —— 此时"事件总线对接 zhuzhao"改为：**apiserver 在事务内同时写主数据 + 历史 + 落一个事件行（Outbox）**，由 zhuzhao 的 L1 消费者/Asynq worker 拉取分发。与 zhuzhao 的 Outbox 范式（ADR-001/ADR-002）完全同构。
- **建议**：方案 F 确认采用时，数据库直接定为 PostgreSQL（而非 MongoDB），使"同事务写主数据+历史"在 PG 单机事务下最自然。

### 24.3 真实业务场景（决策依据）

- **场景1（事件广播）**：activelist 数据变更（如封禁 IP 列表新增 IP）→ 触发预定义事件 → 工单/其他模块订阅响应。属事件驱动、最终一致，**不需跨事务**。
- **场景2（多源摄取）**：工单数据源多样（手动 + 其他模块产生/获取），工单持有对各源的引用/快照，各源独立写、工单独立建，属松一致 ingress，**不需跨事务**。

两场景均不要求 activelist 与 zhuzhao 跨事务一致 → 内化（C3）不成立，独立服务 + 事件总线（C2）为正确形态。

### 24.4 落地待办（gap）

| 编号 | 项 | 归属 | 说明 |
|------|------|------|------|
| **G1** | Mongo → PG 迁移设计 | activelist | 动态集合→分区表/每类型表；Change Stream/watcher→Outbox/逻辑复制；历史快照落 PG 表（建议随方案 F 一起做）。**含日志 writer 迁移**：§19.7.1 的 `mongo_writer.go`/`NewMongoWriteSyncer` 需改为 PG writer，否则转 PG 后日志仍依赖 Mongo |
| **G2** | 事件桥接设计 | 集成 | activelist 变更事件 → zhuzhao **L1 `ticket_events`（事件源）**的桥接，再由 L1 消费者/Asynq worker 分发；与 §23.2 Q1（业务事件订阅能力）同源，需在 Q1 选型时一并确定投递目标为 zhuzhao 事件机制（L1 为源、Asynq 为执行器，见 ADR-001/ADR-002） |
| **G3** | 工单多源 ingress | zhuzhao | 工单模块设计多种数据源适配器（手动 + activelist + 其他模块），activelist 仅其一；属 zhuzhao 工单模块内部设计 |
| **G4** | 两层审计落地 | 双方 | 网关层（zhuzhao E13 跳过 body）+ 业务层（activelist 自脱敏 accesslog）；日志基础设施复用 zhuzhao `pkg/log`（C2' 同仓库直接 import） |
| **E13** | zhuzhao 反向代理模块 | zhuzhao | `app/service/proxy/` + `SetForwardHeaders` + Restrict 资源 `activelist` + accesslog 跳过 body（§19.4） |

> G2 与 Q1 强相关：若 Q1 选 B（webhook）或 C（Asynq），投递目标应指向 zhuzhao 的事件机制而非 activelist 私有队列，以保证"封禁 IP 事件"等能被工单/其他模块统一订阅。
