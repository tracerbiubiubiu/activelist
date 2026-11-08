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
| 查询 | keyset 复合游标分页（created_at DESC, id DESC；无过滤 / 排序 / 聚合） |
| 量级 | 百万行以内 |
| 存储 | **PostgreSQL**：每类型一张表 + `data` JSONB（id 自增 / 乐观锁 / 软删；建类型时建表，字段演进零 DDL） |
| Schema | **方案 D（单一当前版本）**：数据行不带 schema_version；兼容演进零迁移，破坏性变更懒执行（旧数据下次更新 422 + 迁移） |
| 导入导出 | JSON；导入幂等 = **全量替换**（事务内清表重灌、保留源 id；非 upsert、无需业务唯一键）；导出含 id / created_at；并发写由乐观锁保护（跨导入读改写 409，不静默写坏） |
| 数据性质 | **敏感高危数据 → 可靠性**；存储加密**已拍板不做**（2026-09-03，内网双 network + 审计一期不落字段值覆盖当前风险；跨网/合规变化再启）；日志脱敏**暂不做**（`sensitive` 标记 + 统一出口两钩子预留） |

## 文档

- [activelist.md](./docs/activelist.md) —— 原始完整设计方案（2466 行，正文为历史方案）+ 头部**收敛声明 · 最终画像 · 设计定稿补充（设计 SSOT）**
- [implementation-plan.md](./docs/implementation-plan.md) —— **实现计划（M-A）：目标/非目标、验收标准、里程碑、API 修订清单、代码目录、配置（实现 SSOT）**
- [activelist-review.md](./deliverables/activelist-review.md) —— 方案评审（**历史**：针对收敛前方案；数据模型侧发现仍可参考）
- [ADR-003-integration-contract.md](./docs/ADR-003-integration-contract.md) —— 与 zhuzhao 集成契约（**SSOT，2026-09-03 起以本项目为准**；含对 zhuzhao 能力需求汇总；zhuzhao 侧 `docs/adr/ADR-003-activelist-integration-form.md` 为镜像）

## 与 zhuzhao 的关系

- **部署**：独立服务，内网双 network 隔离（`activelist_internal` + `zhuzhao_to_activelist`），仅 zhuzhao 容器可达
- **调用**：zhuzhao 网关统一 JWT / Casbin / Restrict 鉴权，透传 `X-Operator`（操作者）、`X-Request-ID`（链路追踪）
- **日志**：activelist 只记技术 / 运行日志（请求级 + 错误级，含 `X-Request-ID`；脱敏暂不做——钩子预留）；业务 / 审计日志由 zhuzhao 记录

## 快速开始（部署态，M-A6 部署件）

```sh
# 0. 预建跨 compose 共享网络（zhuzhao 网关容器须加入同一网络）
docker network create zhuzhao_to_activelist

# 1. 注入密钥（两项均必填：缺失 compose 直接报错拒建，不给弱缺省值。
#    SK 须与 zhuzhao 网关侧 GATEWAY_SK 同值；应用层对空 SK 亦 fail-closed 拒启）
export ACTIVELIST_CALLER_ZHUZHAO_SK=<与 zhuzhao 网关侧 GATEWAY_SK 同值>
export ACTIVELIST_PG_PASSWORD=<PG 口令>

# 2. 构建镜像 + 起全栈（PG + apiserver×2 多副本 + 每日备份）
docker compose -f deploy/compose.prod.yaml up -d --build

# 3. 健康检查（/apiserver 走内部网络；探针免鉴权）
docker compose -f deploy/compose.prod.yaml exec apiserver \
  wget -qO- http://127.0.0.1:18081/readyz

# 4. 签名调用验证（zhuzhao 侧：GATEWAY_AK/GATEWAY_SK 同值 + upstreams
#    prefix=/al target=http://activelist:18081；经网关 /al/api/v1/... 访问）
```

- 开发态（本地直跑二进制 + 仅 PG 容器）：`deploy/compose.dev.yaml` + `ACTIVELIST_PG_PORT=15432` + `ACTIVELIST_CALLER_ZHUZHAO_SK`（本地任意非空值，如 dev-gateway-sk——空密钥环拒启）；
- 门禁：`make lint` / `make fmt` / `make test` / `make test-integration`（testcontainers 真 PG）/ `make build`；CI（GitHub Actions）覆盖 vet + gofmt + 单测 + 集成（race）；
- 接口清单 / 配置项：[implementation-plan.md §4/§6](./docs/implementation-plan.md)；
- **备份与恢复**：[deploy/backup/README.md](./deploy/backup/README.md)（每日 pg_dump + WAL 归档，保留 14 份）。

## 状态

- 文档就绪：设计收敛定稿 + **实现计划就绪**（[implementation-plan.md](./docs/implementation-plan.md)，M-A 验收标准见其 §2）
- 代码进度（2026-09-09）：**M-A1–M-A5 已交付**（骨架 / 类型注册 / CRUD / Schema 演进 / 导入导出），**M-A6 代码与部署件完成**（AK/SK 验签 + X-Operator + 统一访问日志 + Dockerfile/部署态 compose/备份；实测随部署批）——详见 implementation-plan 里程碑表
- 2026-09-16：验签切生态统一形态（utils `GinMiddleware` + `response.AKSKFail()`，归因键常量化）；访问日志补 `caller` 归因字段（pin v0.4.1）
- 2026-09-29（P4 W5 前置批）：三端点 zhuzhao 风格整改——`/deprecate`·`/schema`·`/restore` 标识入 body 路径静态动词段（zhuzhao 000032 迁移同步 menu_apis；消费面零破坏——网关纯透传）；zhuzhao-ui al 两页（types/data：cursor 分页+动态 schema 列+导入导出）已上线消费（Phase 4 收官）
- 启动前置 ✅ **已就绪**：本审计修复候选 pin zhuzhao-utils **`v0.4.4-0.20261106080630-cd759a040245`，直引无 replace/GOWORK**（保留 Reader 错误分档，承接 B07-F10 Transport 生命周期修复；不可变远端提交与 sum 验证见 ADR-003，主线/tag/部署待承接）。activelist 硬依赖 `logger` + `postgres` + `response` + `aksk`；**部署 fail-closed**：应用对空 SK 拒启（config 层校验，覆盖 `${VAR:-}` 展开为空的形态）；deploy/compose 带 dev-gateway-sk 缺省便于本地起栈，生产务必 env 覆盖
- 排期归属：zhuzhao Phase 3 主线 **M-A（activelist 独立实现）**，与其他里程碑无链式依赖（2026-09-02 design-decisions §23.2，详见 ADR-003「排期与集成拆分同步」节）

## 提交规范

遵 zhuzhao `docs/standards.md` §12-9 / §12-10；关联主仓复核报告
`deliverables/software-company/zhuzhao-report-followup-2026-11-08.md` 的 **RR-GIT-01**（2026-11-08）。
本仓脚本与主仓同源、自包含，仅依赖 Git + Python 3 标准库，不依赖主仓目录或新增 Go/npm 包。
主题为 `type(scope): 摘要`（scope 可省略），摘要去前缀后最多 50 个 Unicode 字符、不得以句号结尾；
主题与正文间须空行，正文不得为空或只有空白。所有新 merge 提交也须显式提供规范主题与正文。
修复正文仍按公约填写「改动摘要 / 影响面 / 验证证据」与同类扫描，硬检查①–⑤不替代人工验证证据。

```sh
make hooks              # 新 clone / 新工作树开工时安装
make commit-lint        # origin/当前分支..HEAD；初推无同名远端时从启用基线检查
make test-commit-lint    # 临时独立仓库黑盒反例，不触碰业务栈
# 显式历史审计（完整 SHA；不豁免历史）
python3 scripts/commit-policy.py range --before <完整SHA> --after <完整SHA> --strict
```

`make hooks` 启用 Git 的 worktree 配置，仅在当前工作树设置相对 `.githooks`；common
`core.hooksPath` 保留，尚未承接本批的 main/其它工作树不被改装。保留在途显影与跨主题纯重题
amend 保护，`commit-msg` 先挡消息违规，`pre-push` 按 Git 提供的每个 ref 新旧 SHA 检查完整窗口。
`GIT_INDEX_FILE` 保持原值，兼容显式 pathspec 提交，不暂存或重写其它会话文件。

固定启用基线见 `scripts/commit-policy-baseline`：仅其祖先为已推送存量，不改史、不自动前移。
CI push 使用事件 before/after，PR 使用真实 base/head SHA，初推与 force push 均有覆盖；
缺对象精确获取仍失败、非法 SHA 或浅克隆一律拒绝，不回退近 20 笔。现有代码测试 CI 保持。
