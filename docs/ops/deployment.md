# activelist 部署 runbook

> 部署态 = `deploy/compose.prod.yaml`；开发态 = `deploy/compose.dev.yaml`（仅 PG，服务跑宿主 `go run ./cmd/apiserver`@18081）。
> 集成契约 SSOT：[ADR-003](../adr/adr-003-integration-contract.md)；跨仓拼装见主仓[部署与联调](https://github.com/tracerbiubiubiu/zhuzhao/blob/phase5/docs/ops/deployment.md)。

> 以下compose命令从仓库根目录 `cd deploy` 后执行；不要在docs/ops中执行。

## 运行基线升级（2026-11-12 候选）

应用构建使用 Go 1.27.4 / Alpine 3.24，运行镜像 Alpine 3.24.4；PostgreSQL 18.6 使用 Debian trixie 官方镜像，备份工具与服务端同版本。镜像均钉完整版本和多架构摘要；本候选不自动修改已有实例。版本选择、依赖变化和验证边界见 [升级记录](version-upgrade.md)。

PG18 的 `PGDATA=/var/lib/postgresql/18/docker`，Compose 将新的 `pgdata18` 卷挂到 `/var/lib/postgresql`。旧 PG15 `pgdata` 不能直接挂给 PG18；新卷为空不代表旧数据已迁移。已有开发/部署栈先盘点项目、容器、卷、备份与写者，停写后用逻辑备份恢复至独立 PG18，核验数据和业务再切连接。保留旧卷和旧环境；不要执行 `down -v`。切换后若新库已接收写入，回指旧库会丢新写，需重新安排数据迁移，不能称为无损回滚。

PG18 的 `wal_archive18`、`backups18` 与旧卷分开。旧版本 WAL/物理基备只用相同 PG 主版本恢复；升级后重建物理基备和连续 WAL 链，不能将旧 WAL 接到新库。逻辑迁移保留 revision sequence 及其状态；切换后要求客户端重读，避免沿用旧 revision。

## 快速开始（部署态）

```sh
cd deploy

# 0. 预建共享网（一次性；zhuzhao 栈所在宿主）
docker network create zhuzhao_to_activelist

# 1. 注入必填变量（本地 .env，已 gitignore；两项均 fail-closed 缺失拒建）
cat > .env <<'ENV'
ACTIVELIST_CALLER_ZHUZHAO_SK=<与 zhuzhao 侧 GATEWAY_SK 同值>
ACTIVELIST_PG_PASSWORD=<强口令>
ENV

# 2. 起栈（首次构建镜像；apiserver 双副本无状态，迁移随启动自跑+advisory lock 并发安全）
docker compose -f compose.prod.yaml up -d --build

# 3. 验证
docker compose -f compose.prod.yaml ps            # 三服务 healthy
# 生产不发布宿主端口，直接从栈内验证：
docker compose -f compose.prod.yaml exec apiserver wget -qO- http://127.0.0.1:18081/healthz
```

## 与 zhuzhao 的接线（唯一两件事）

1. **网络**：本栈 `zhuzhao_to_activelist`（external）与本栈 apiserver 别名 `activelist`——zhuzhao app 容器须加入同一网络（其 `deployments/docker-compose.yaml` 已默认挂接）。
2. **密钥对值**：`ACTIVELIST_CALLER_ZHUZHAO_SK` = zhuzhao 侧 `GATEWAY_SK`（HMAC 出站签名验签）。zhuzhao 网关默认 target `http://activelist:18081`，无需改配置。

## 运维要点

- **大文件导入**：import先使用utils统一流式验签，暂存于系统临时目录（容器默认`/tmp`，可通过`TMPDIR`选择已有可写挂载目录）；每请求0600私有文件，内存验签窗口固定，磁盘占用最高为配置上限外加一字节探读。空间按并发数×`ACTIVELIST_BUSINESS_IMPORT_MAX_BYTES`与余量准备，失败503从服务端日志排查磁盘。正常成功/拒绝/业务失败/panic展开均清理，SIGKILL/宿主崩溃残留由系统临时目录清理策略承接。`read_timeout`须允许上传；`write_timeout`与既有HMAC时间窗还须覆盖处理耗时，普通CRUD上限仍16MiB。
- **扩缩容**：`docker compose -f compose.prod.yaml up -d --scale apiserver=N`（无状态；共享网别名天然负载到全部副本）。
- **备份**：pgbackup 每日 02:00 `pg_dump -Fc` 保留 14 份 + WAL 归档（`archive_timeout=300s`）——恢复步骤见 [备份与恢复](backup.md)。
- **健康**：`/healthz`（存活）/`/readyz`（就绪，检 PG）；compose healthcheck 已接。
- **已知边界（触发驱动挂起项）**：无 metrics 端点（观测=结构化日志+健康探针，ES 聚合线触发时补）；容器日志落容器内 `logs/` 未挂卷（重建即丢，聚合前可接受）。


## 文档 revision 迁移（000002，2026-11-05）

升级前先完成数据库备份，并停止全部旧 apiserver 写实例；旧代码仍按单行+1或导入置1分配版本，不能与新代码混跑。构建/部署新版本时由启动迁移统一锁定存量动态表，将持久revision序列初始化到所有存量version与当前全库事务号之上；后者保护已清空旧表仍被客户端保留的历史version（旧API每事务对同一行至多递增一次）。全新无类型库没有历史数据版本，序列从1开始。迁移完成后再开放新实例流量。迁移000002只修改默认值和后续分配方式，保留现存行数据与version。

`version`仍为数值字段，后续创建、更新、软删、恢复、导入分配全库新revision；空导入和ID重用不会重启revision。客户端须使用响应/重读的version，不自行+1。备份/恢复须包含sequence状态；禁止手工重置revision序列。回滚须停止新写实例并同批回退代码与000002数据库变更，不能单独删除sequence后继续运行新程序。

该保证限于同一数据库连续运行及排空旧写者的正常原地升级。手工历史改版、逻辑恢复导致事务历史丢失、恢复到旧备份或down/up重新建序列不能保证与所有旧客户端缓存隔离；这些操作须先隔离写入口并要求客户端重读，不能沿用旧revision盲写。本迁移没有跨数据库的外置版本世代。
