# activelist 实现计划（zhuzhao Phase 3 · M-A）

> **状态**：2026-09-03 建档，**实现 SSOT**。定稿依据 = [`docs/activelist.md`](./activelist.md) 头部（收敛声明·最终画像·设计定稿补充）+ [`ADR-003-integration-contract.md`](./ADR-003-integration-contract.md)（集成契约 SSOT）。zhuzhao 侧排期挂 M-A（phase3/13），其验收标准引用本文件 §2（M-A 验收 = 本文件验收标准）。

---

## 1. 目标 / 非目标

**目标**（= 职责收敛定稿，全部且仅此）：

| # | 能力 | 定稿口径 |
|---|------|---------|
| 1 | 类型注册 | 任意自定义类型；字段类型 = `int` / `string` / 二者的**列表**；typeName 与字段名白名单校验 |
| 2 | Schema 演进 | **方案 D**：单一当前版本；兼容变更（加 optional / 放宽）零迁移；破坏性变更允许提交、懒执行（旧数据下次更新 422） |
| 3 | 动态字段校验 | 写路径全量校验（读旧行 → 合并 → 对当前 schema 校验）；读取零校验 |
| 4 | 数据 CRUD | PG 每类型一表 + `data` JSONB；id 自增（BIGSERIAL）；乐观锁（version）；软删保留 + 恢复 |
| 5 | 查询 | 仅 id 分页 + 创建时间倒序（无过滤 / 排序参数 / 聚合） |
| 6 | 导入导出 | JSON；导入 = **全量替换**（单事务清表重灌、保留源 id、version 重置 1、setval 序列）；幂等；并发由乐观锁保护 |
| 7 | 技术日志 | slog 文件日志（复用 zhuzhao-utils `logger`）；请求级（访问日志记 method/path/operator/trace_id/参数 4KB 截断）+ 错误级；含 `X-Request-ID`；**脱敏暂不做**（已拍板，预留 schema `sensitive` 标记 + 统一日志出口两个钩子，见 ADR-003 审计节） |

**非目标**（明确不做，边界）：用户认证/鉴权（zhuzhao 网关统一；**服务间 AK/SK 验签除外**——M-A6 交付项）、事件发布（zhuzhao 业务操作点显式发布）、业务审计（zhuzhao 侧记录）、历史快照、过滤/排序/聚合查询、嵌套对象/关系/公式字段、存储加密（**已拍板不做**，2026-09-03）、多租户、物理删除 API。

## 2. 验收标准（M-A 退出标准）

| # | 验收项 | 通过条件 |
|---|--------|---------|
| A1 | 类型注册 | 注册 → 建表 + 元数据落库；重复注册 409；typeName / 字段名非法 422；deprecated 类型拒绝写入 |
| A2 | CRUD | 插入/按 id 查/更新/软删/恢复全通；软删后默认不可见、更新 409、重复软删幂等；恢复后可写 |
| A3 | Schema 演进 | 加 optional 字段后旧数据零迁移可读可写；加 required 字段后新插入强制校验、旧数据更新返回 422（错误信息含迁移指引） |
| A4 | 乐观锁 | 并发更新同一行 → 恰一成功，其余 409；version 不匹配更新 409 |
| A5 | 导入导出 | 导出完整文档 JSON 数组（含软删行）→ 清空环境 → 导入 → 数据一致；**重导同一文件结果一致（幂等）**；序列正确（后续插入不冲突）；导入期间并发写：短阻塞（事务级 `SET LOCAL lock_timeout` 5s）后等至提交或快速 409，并发导入按类型互斥（表锁 / advisory lock） |
| A6 | 日志 | 请求级 + 错误级日志含 `X-Request-ID` 与 `X-Operator`；**脱敏暂不做**（已拍板，预留 schema `sensitive` 标记 + 统一日志出口钩子，见 ADR-003 审计节）；不记业务语义内容 |
| A7 | 部署 | docker-compose 双 network（`activelist_internal` + `zhuzhao_to_activelist`）、**apiserver 双副本**；`/healthz` `/readyz`（readyz 检 PG）；优雅停止（SIGTERM 排空，重启单副本服务不中断）；**migrations 全库只执行一次**（随启动执行，工具 = **golang-migrate** iofs embed，postgres 驱动自带会话级 advisory lock，多副本并发启动不重复执行）；备份任务按日跑通 |
| A8 | AK/SK 验签 | 缺/错签名 → 401；密钥环空（或含空 SK）拒绝启动（fail-closed）；`X-Operator` 在签名覆盖内（不可伪造） |
| 门禁 | 工程 | `make lint` / `make test` 全绿；CRUD + 演进 + 导入导出有针对真实 PG 的集成测试 |

## 3. 里程碑拆分

> 总量锚定 zhuzhao 侧估算 **1.5–3 人日（⚠️ 待校准）**；串行依赖如下，M-A4/M-A5 可并行于 M-A3 之后。

| 里程碑 | 内容 | 依赖 | 退出标准 |
|--------|------|------|---------|
| M-A1 骨架 | go.mod（引 zhuzhao-utils）、config 加载（viper yaml+env）、**`internal/app` Wire DI 装配 + 优雅停止**（工程结构基线 §5 注）、**Makefile 门禁（lint=vet+gofmt / test / test-integration（真 PG）/ build）**、migrations 000001（元数据表）、docker-compose（PG）、healthz/readyz | ✅ **已实施（2026-09-08，feat/ma1-skeleton）**：utils **v0.2.0 直引无 replace**；`${VAR:-default}` 展开 + `ACTIVELIST_*` 显式 env 绑定双通道（无 yaml 纯 env 可跑）；wire_gen 手工同步（taskrunner 同款惯例）；000001 = `data_types` + `data_type_schema_history`（迁移随启动执行，pgx 驱动 advisory lock 幂等，多副本安全）；compose 起 PG（**postgres:15-alpine**，16 拉取受当前网络限制，升级随部署复盘）；部署形态微调：config 走 utils postgres 字段形态（§6 的 url 草图弃用）。实测：healthz/readyz 双探针 + SIGTERM 优雅停止 + 迁移集成测试（testcontainers）全过 | 服务起 + 健康检查过 + lint/test 绿 ✅ |
| M-A2 类型注册 + 建表 | 元数据表（类型 + 当前 schema + 变更记录）、CREATE TABLE、字段定义校验（int/string/列表、白名单、保留字段） | ✅ **已实施（2026-09-08，feat/ma1-skeleton）**：`internal/meta`（data_types 读写 + 变更历史只追加）+ `internal/validation`（type ∈ int/string/int_list/string_list、名称 `^[a-z][a-z0-9_]{0,62}$`、保留字段 8 个分 RESERVED_FIELD 档；**系统表名 3 个（data_types/data_type_schema_history/schema_migrations）+ pg_ 前缀 + ≤51 字符上限**（2026-09-08 复查补：typeName 即动态表名，注册系统表名会经 IF NOT EXISTS 静默跳过建表、数据读写直打元数据表；>51 会使 idx_<name>_created 超 PG 标识符 63 限被截断、长前缀重名类型静默共享索引））+ `internal/repository` 动态建表（保留列族 + data JSONB + (created_at,id) 倒序索引）；**注册事务化**（元数据+DDL+历史同事务原子提交，PG 事务化 DDL）；端点 POST/GET /admin/types、GET /:typeName、POST /:typeName/deprecate（幂等）；信封 = §6.8 `{code(=HTTP状态), msg, data, detail.error_code}` 原样落地；字段类型标识符 `int_list/string_list` 为实现定名（文档只写「二者的列表」，2026-09-08 实现拍板）；"deprecated 拒绝写入" 的数据路径接线随 M-A3。集成测试 4 组（生命周期/负向分档/废弃流/HTTP 信封 e2e）全过；**2026-09-08 二次复查修复**：① operator 空串经 COALESCE 回退 'system'（原 NULLIF 空串产显式 NULL，不触发列 DEFAULT 直报 23502——M-A6 后 X-Operator 缺头即 500）② deprecate 旧态读改走 tx 与写同快照（原 pool 读在事务外，M-A4 演进上线后历史 fields 可能取旧值）③ 字段名错误消息 ≤51→≤63（复制 typeName 消息未改数，regex 实际上限 63） | A1 ✅（除 deprecated 拒写随 M-A3 接线） |
| M-A3 CRUD | 插入 / 列表（keyset 分页 + created_at 倒序）/ 单查 / 更新（读-合并-全量校验-乐观锁）/ 软删 / 恢复 | ✅ 已实施（2026-09-08，feat/ma1-skeleton）：repository/data.go（Document 完整文档——写接口统一返回变更后行；dbtx 最小接口 pool/tx 共用；复合游标 ListDocs）+ service/data_service.go（更新 = FOR UPDATE 行锁内读-合并-全量校验-version 校验）+ validation/data.go（键 ⊆ schema / required 非空 / 四类型值校验 / null=未提供；未知键 422、保留键 RESERVED_FIELD 档）+ /api/v1/data 6 路由；实现拍板：① body 信封 {data} / {data,version}（data 与保留列族在 JSON 形态隔离）；② 软删 = status 列 deleted（表无 deleted_at），每次变更（内容或状态）version+1——状态变更入乐观锁语义，旧文档立即过期；③ 重复软删/恢复幂等返回现态；④ 软删行单查可见、列表默认排除（§7）；⑤ 游标 = 上一行 (created_at,id)，after_created_at+after_id 成对缺一 400，page_size 钳制 [1,max] 并回显生效值，满页才出 next_cursor；⑥ deprecated 拒写范围 = 插入/更新（内容写），软删/恢复放行（存量清理不被堵）；⑦ version 绑定层 required（400）+ service 兜底。集成测试 7 组（生命周期/软删流/404 负向/A4 乐观锁含 6 并发恰一成功/废弃拒写/HTTP 信封+钳制）+ validation 单测 16 案 + handler 绑定负向 10 案；dev PG 实机冒烟（健康探针/注册/插入/游标 400/SIGTERM 优雅停止）全过 | A1 全齐（deprecated 拒写接线）/ A2 ✅ / A4 ✅ |
| M-A4 Schema 演进 | 演进端点 + 方案 D 语义（兼容 / 破坏性懒执行）+ schema 变更历史查询 | ✅ **已实施（2026-09-09，feat/ma1-skeleton）**：`POST /admin/types/:typeName/schema`（全量定义 + 元数据行 version 乐观锁 CAS——后到者 409 CONFLICT 须重读重提；废弃类型拒演进 409；UpdateSchema 谓词含 `status='active'`——SQL 层双保险，废弃后拒演进）+ `GET /admin/types/:typeName/history`（register/evolve/deprecate 三 op，新→旧）；**懒执行错误契约落码**（§4 FIELD_DEPRECATED / NEW_REQUIRED_FIELD）：更新路径缺演进新增必填字段 → 422 NEW_REQUIRED_FIELD（msg 含迁移指引，A3）；已移除字段按**字段史判定**（历史 schema 曾含 = FIELD_DEPRECATED 待清理，真拼写错误维持 VALIDATION_ERROR）——validation 以 detail.reason 分档、service 层映射（插入路径缺必填保持 VALIDATION_ERROR）；meta.UpdateSchema/ListHistory/FieldExistedInHistory。集成 6 组（optional 兼容/required 懒执行/移除懒执行/乐观锁冲突+废弃拒演进/历史/HTTP e2e）+ 单测 reason 分档；dev PG 实机冒烟（演进/409/历史/懒执行 422 全链）通过 | **A3 ✅** |
| M-A5 导入导出 | 导出（含 id/status/created_at）/ 全量替换导入（同事务分批写入 + setval）/ 批次审计素材（响应返回批次汇总） | ✅ **已实施（2026-09-09，feat/ma1-skeleton）**：`GET /data/:typeName/export`（流式写裸 JSON 数组——实现拍板：信封包文件体破坏流式与导出/导入对称性；含软删行、id 序稳定）+ `POST /data/:typeName/import`（单事务：`LOCK TABLE SHARE ROW EXCLUSIVE`（§7 审计修正——防并发 INSERT 漏过 + 按类型互斥）→ 清表 → `transfer.DecodeBatches` 流式分批（`import_batch_rows` 配置）→ 逐行对当前 schema 全量校验 → `pgx.Batch` 重灌（保留源 id/status/时间戳/操作者，**version 重置 1**）→ `setval(max+1, false)`）；**并发保护**：常规写路径（插入/更新/软删/恢复）事务级 `SET LOCAL lock_timeout='5s'`，55P03 → 409 CONFLICT 快速失败——以 SET LOCAL 实现（连接池无状态泄漏），ADR-003 D1「utils postgres.Config 加 LockTimeout 字段」遗留项以此口径关闭；**分档**：废弃类型拒导入 409、文件内重复 id 422（PK 23505 映射）、校验失败整笔回滚、非数组/截断文件 400。集成 7 组（往返一致/幂等/校验回滚/并发快失败/空数组清空+废弃拒/HTTP e2e/并发导入互斥）+ transfer 单测 5 组；`-race` 干净；dev PG 冒烟全链（导出含软删/导入汇总 rows/deleted/max_id/重导幂等/序列 id=3）通过 | **A5 ✅** |
| M-A6 日志 + 部署收尾 | slog 接入（utils `logger`）、访问日志（**统一中间件出口**：method/path/operator/trace_id/参数 4KB 截断/结果；脱敏暂不做）、**AK/SK 验签中间件**（utils `aksk`，验 zhuzhao 调用签名；验收 A8；**依赖 utils 发 v0.2.0（含 aksk），否则临时 go.mod replace**）、compose 双 network（**多副本**）、备份策略（pg_dump 每日 + WAL 归档）、README 快速开始。**✅ 代码件已实施（2026-09-09）**：`AKSKAuth`（utils `aksk.GinMiddleware` + **`response.AKSKFail()` 统一形态**——2026-09-16 验签统一批：自写 onFail 退役，401+10002/413+10001/400+10001 分档中文文案与 caller/operator 归因键由 utils 承接，AKSKAuth 增 logger 参数透传 `Verifier.Logger`；`MaxBodyBytes=ImportMaxBytes`，防 aksk 默认 8MB 拦大导入；原独立 `Operator` 中间件退役——X-Operator→ctx 由 GinMiddleware 验签后写入，缺省 system 口径不变）+ `AccessLog`（统一出口 4KB 截断，operator 缺失兜底 system；**错误级出口**：消费 `c.Errors` 出 ERROR 行——export 流中途截断不再服务端无日志）+ handler 7 写路径 operator ctx 化 + **wire 层空密钥环拒启**（fail-closed，对齐 taskrunner C2；handler 层空环为请求级 401，测试构造零签名直连）；中间件单测 8 项（签名通过/错钥/缺头/空环/413/时间窗/body 还原/日志行）；lint+单测+集成 `-race` 全绿。**✅ 部署件已实施并部署实测（2026-09-09/10）**：`deploy/compose.prod.yaml`（双 network：`activelist_internal` + external `zhuzhao_to_activelist`；apiserver `replicas: 2`；pgbackup 每日 02:00 `pg_dump` 保留 14 份 + WAL 5 分钟归档；恢复步骤见 `deploy/backup/README.md`）+ Dockerfile + README 快速开始；部署实测抓修三例——副本缺省口令未透传重启循环、镜像日志目录属主、共享网 DNS 别名。备份任务按日配置就绪，部署环境首次成功备份落盘后 A7 尾项全闭 | 全部 | A6 / A7 / A8 |

## 4. API 清单（收敛后修订版，**取代 activelist.md §6.9 旧清单**）

> 旧清单中 `/history` 数据端点移除（审计归 zhuzhao）；列表查询砍掉 filter/sort；新增导入导出与恢复；**写接口（插入/更新/删除/恢复，统一 POST 子路径——2026-09-09 实现拍板：`/:id/update` / `/:id/delete` / `/:id/restore`）统一返回变更后完整文档**（id、version、data、updated_at——审计契约素材，见 ADR-003 收敛修订）。

**类型管理**（zhuzhao 侧 Restrict 映射 `activelist:admin`）：

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/v1/admin/types` | 注册新类型（含字段定义） |
| GET | `/api/v1/admin/types` | 类型列表 |
| GET | `/api/v1/admin/types/rules` | 类型与字段创建规则（前端弹窗展示用） |
| GET | `/api/v1/admin/types/:typeName` | 当前 schema 定义 |
| GET | `/api/v1/admin/types/:typeName/history` | **schema 变更历史**（元数据侧，非数据快照） |
| POST | `/api/v1/admin/types/:typeName/schema` | Schema 演进（方案 D 语义） |
| POST | `/api/v1/admin/types/:typeName/deprecate` | 废弃类型 |

**数据 CRUD + 导入导出**（Restrict 映射 `activelist:read` / `write`）：

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/v1/data/:typeName` | 插入数据 |
| GET | `/api/v1/data/:typeName` | 列表：仅 keyset 分页（`?after_created_at=<RFC3339>&after_id=<int>` **成对出现，缺一 400** / `?page_size=`）+ created_at DESC, id DESC 倒序 |
| GET | `/api/v1/data/:typeName/:id` | 查单条 |
| POST | `/api/v1/data/:typeName/:id/update` | 更新（body 携带 version，乐观锁） |
| POST | `/api/v1/data/:typeName/:id/delete` | 软删除 |
| POST | `/api/v1/data/:typeName/:id/restore` | 恢复软删数据（最终画像「高危数据误删可恢复」） |
| GET | `/api/v1/data/:typeName/export` | 全量导出 JSON 数组（流式；完整文档，含软删行——否则导出→导入会丢失软删数据） |
| POST | `/api/v1/data/:typeName/import` | 全量替换导入（与导出同构的 JSON 数组 body；响应返回批次汇总：行数/耗时/max id） |

~~`GET /api/v1/data/:typeName/:id/history`~~ — 移除（数据变更历史 = 审计，归 zhuzhao）。

**错误码**：现行信封 = standards §3.3 `{code, message, data, request_id}`（数值业务码映射见 zhuzhao 仓 `docs/api/errcode.md` §4——`VALIDATION_ERROR`→100000→HTTP 422 等）；`FIELD_DEPRECATED` / `NEW_REQUIRED_FIELD`（懒执行提示迁移）/ `CONFLICT` 语义保持。

## 5. 代码目录结构（预定）

```
activelist/
├── cmd/
│   └── apiserver/main.go      # 唯一二进制（收敛后单进程）
├── internal/
│   ├── config/                # 配置加载（${VAR} 环境变量展开，对齐 zhuzhao 模式）
│   ├── meta/                  # 类型注册 + Schema 定义/演进（元数据表读写）
│   ├── repository/            # 每类型表管理（CREATE TABLE）+ CRUD + 乐观锁 + 软删（层名对齐基线；与 utils postgres 不同包路径不冲突）
│   ├── validation/            # 字段校验（int/string/列表、白名单、保留字段、schema 合法性）
│   ├── apperr/                # 业务错误（字符串码 + 数值映射 100000 跨服务段 + Detail 折叠）
│   ├── transfer/              # 导入导出（全量替换、导出、序列 setval）
│   ├── handler/               # HTTP 层（薄：绑定/映射，业务在 service——微服务结构基线）
│   ├── service/               # 业务层（类型管理/CRUD 编排/导入导出事务）
│   ├── middleware/            # requestid / 技术 access 日志（统一出口）/ AK-SK 验签
│   └── app/                   # **Wire DI** + 装配、启动、优雅停止（对齐 zhuzhao/taskrunner）
├── migrations/                # 独立库独立编号（000001 起；与 zhuzhao 迁移号无关）
├── configs/config.yaml
├── deploy/
│   ├── compose.prod.yaml      # 部署态：PG + apiserver×2 + pgbackup；双 network，apiserver 仅对 zhuzhao network 暴露 8080
│   ├── compose.dev.yaml       # 开发态：仅 PG（apiserver 本地直跑）
│   └── backup/                # pgbackup 每日备份 + WAL 归档（backup.sh + 恢复步骤 README）
└── Makefile                   # lint（vet+gofmt）/ fmt / test / test-integration（真 PG）/ build
```

命名注意：`internal/` 下包名避开与 zhuzhao-utils 同短名引起的 import 混淆；数据访问层统一 `repository`（基线层名，taskrunner 同款）。

> **工程结构基线（2026-09-04 所有者拍板，zhuzhao 16 号 §9）**：以正式微服务标准建设——
> **Wire DI**（装配收敛 `internal/app`）、**handler → service → repository 分层**、
> yaml+env 配置、统一 Makefile 门禁（lint=vet+gofmt / test / test-integration（真 PG）/ build）、优雅启停；
> taskrunner 已于同日完成同规格重构（其 commit ca1a283 可作结构参照）。

## 6. 配置（收敛后，取代 activelist.md §18.4 旧配置）

```yaml
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s        # 导入大文件需要长写超时

postgres:
  host: ${PG_HOST:-postgres}
  port: 5432
  user: ${PG_USER:-activelist}
  password: ${PG_PASSWORD}
  dbname: activelist
  max_open_conns: 10        # 多实例时按 副本数 × max_open_conns < PG max_connections 估算

log:                          # 对齐 zhuzhao-utils logger 的 LogConfig（级别/目录/轮转）
  level: info
  dir: logs

business:
  page_size_default: 20
  page_size_max: 100
  import_batch_rows: 1000    # 全量替换导入同事务内的分批行数
  import_max_bytes: 1073741824  # 导入 body 硬上限（§7 大文件配套，MaxBytesReader）

security:                     # AK/SK 验签（基线 §9，M-A6 中间件；形态对齐 taskrunner）
  callers:                    # 预期调用方 AK→SK 密钥环（当前唯一调用方 zhuzhao）
    zhuzhao: ""               # env ACTIVELIST_CALLER_ZHUZHAO_SK（空密钥环拒绝启动 fail-closed）

tz: Asia/Shanghai             # 容器时区（基线 §9，compose environment TZ + 镜像 tzdata）
```

旧配置中的 mongo / redis / asynq / log_db 段全部移除（依赖已砍）。
注：activelist 无出站调用 zhuzhao 的场景 → 只需验签密钥环（callers），无 self 签名身份。
注：早期 §6 草图用 `postgres.url` 单字段，实现定为 utils postgres.Config **分字段形态**（DSN 由 utils 构造、密码经 URL 转义）——示例已与实现对齐（2026-09-08）。

## 7. 实现注意点

- **全量替换导入**：单事务内 `DELETE` 全表（含软删行）→ 分批 INSERT（`import_batch_rows`）→ `setval` 至 max(id)；百万行级注意 WAL 膨胀与锁时长，导入为低频运维级操作可接受，事务内分批控制内存。
- **导入大文件**：百万行 JSON 需**流式解析**（JSON 数组流式解码或 NDJSON），避免整包载入内存；HTTP body 大小上限与 `write_timeout` 配套调整。
- **导出必须含软删行**（带 status），否则导出→导入闭环会丢软删数据，违背「软删保留」。
- **分页**：keyset 游标按**完整排序键**比较（审计修正 2026-09-04：原 `WHERE id > $1` 在 id 序 ≠ created_at 序时跳行/重行——导入保留源 id 与 created_at 后必然失序）：`WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT n`，游标 = 上一行 (created_at, id) 二元组（`after_id` 扩为 created_at+id 复合游标）；索引 `(created_at DESC, id DESC)`；offset 深翻页在百万行下不可用。
- **元数据并发注册**：typeName 唯一索引，并发注册后到者 409。
- **访问日志（审计配合）**：统一中间件出口记录 method / path / operator / trace_id / 请求参数（4KB 截断）/ 结果状态；schema 字段定义格式**预留 `sensitive: true` 标记**（暂不实现脱敏逻辑；启用时只改日志层一处，客户端契约不变）。
- **X-Request-ID 透传**：所有响应回显 `X-Request-ID` 响应头（调用方与 zhuzhao 审计行关联排障用）。
- **X-Operator 缺失兜底**：`"system"`（沿用 §15.4）；导入操作者 = 请求头操作者。**X-Operator 入 AK/SK 签名覆盖**（2026-09-03 基线修订：不可伪造；utils `aksk` 验签随 M-A6）。
- **Schema 缓存（多实例就绪）**：进程内 TTL 缓存（60s）或先不做缓存（元数据表 PK 查询足够便宜，当前量级可承受）——**不引入跨实例广播**。演进为低频操作，TTL 窗口内个别实例仍按旧 schema 校验（如新增 optional 字段后短暂 422）属预期行为，不额外处理。
- **并发 Schema 演进**：元数据行带乐观锁——后到者 409，须重读最新定义后重提（**全量定义、非字段级 merge**；低频演进建议串行操作，沿用原 §6.4 语义）。
- **导入期间并发写保护（2026-09-11 实施校准）**：常规写路径与导入事务**均为事务级 `SET LOCAL lock_timeout='5s'`**（导入自身亦设——防并发导入第二等待者无界挂起；连接池无状态泄漏），55P03 → 409 快速失败。（utils `postgres.Config` LockTimeout 字段遗留项以此口径关闭，ADR-003 D1）**全量替换的并发 INSERT 漏洞（审计修正 2026-09-04）**：`DELETE` 全表只锁既有行，事务期间并发 INSERT 的新行不被阻塞、替换提交后存活——破坏全量替换语义与 A5 幂等；导入事务开头须取锁：`LOCK TABLE <tbl> IN SHARE ROW EXCLUSIVE MODE`（阻塞并发写不阻塞读）或按类型 `pg_advisory_xact_lock`（兼防两并发导入互踩）。
- **Insert 重试重复（已知限制，接受）**：无业务唯一键 + 自增 id，调用方重试 POST 会产生重复行；调用方为 zhuzhao 统一 client 层，如需可后置加 `Idempotency-Key`（RFC draft / Stripe 模式），activelist 侧不实现。
- **备份（敏感高危数据必须）**：activelist PG 每日基线备份（pg_dump）+ WAL 归档（PITR）；导入等高危操作前建议先快照。备份策略随 M-A6 写入部署文档。
- **保留字段**：`id` / `version` / `status` / `created_at` / `updated_at` / `created_by` / `updated_by` / `data` 禁止用户 schema 使用。
- **软删行查询**：列表默认排除；单查按 id 可见（带 status 标注）——便于审计对账与恢复操作。

## 8. 开放项（实现前/上线前需关闭）

| # | 项 | 状态 | 备注 |
|---|----|------|------|
| O1 | 审计落点机制 | ✅ 已拍板（2026-09-03） | 双侧记录 + `X-Request-ID` 关联（zhuzhao 审计正本 / activelist 访问日志）；**脱敏暂不做**（风险接受 + 两个零成本钩子）；机制全貌见 ADR-003「审计落点机制」节；activelist 侧义务 = 访问日志（M-A6）+ 写接口返回完整文档（已定稿） |
| O2 | utils 依赖面核对 | ✅ 已验证（2026-09-03） | `logger`/`postgres` 已 config 解耦可直接使用；`errcode`/`response` API 满足统一响应包装（`detail.error_code` 字段 activelist 侧自行适配）；遗留不阻塞项：utils 的 `logger`/`postgres` 无单测（可选补，见 ADR-003 D1 验证记录） |
| O3 | 存储加密 | ✅ 已拍板不做（2026-09-03） | 内网部署 + 日志脱敏 + 审计一期不落字段值已覆盖当前风险评估；若未来跨网部署或合规要求变化再启用（届时另立决策） |
| O4 | E13 反代 | ✅ 已实现（zhuzhao 批次 B，2026-09-09）/ 待联调 | 不阻塞开发；阻塞联调与上线 |

> **2026-09-04 全仓审计修正**：① keyset 分页谓词改完整排序键比较（原 `id > $1` 在导入保留源 id/created_at 后必失序）；② 导入全量替换补表锁/advisory lock（DELETE 只锁既有行，并发 INSERT 漏过破坏替换语义）；③ TZ 入配置；④ `storage`→`repository` 对齐基线层名；⑤ ADR-003 三处「落点机制待定」残留关闭 + G2 死机制划线 + 关联文档断链加仓库限定。
