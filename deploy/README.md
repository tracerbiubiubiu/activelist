# activelist 部署 runbook

> 部署态 = `compose.prod.yaml`（本目录）；开发态 = `compose.dev.yaml`（仅 PG，服务跑宿主 `go run ./cmd/apiserver`@18081）。
> 集成契约 SSOT：[ADR-003](../docs/ADR-003-integration-contract.md)；跨仓拼装总 runbook见 zhuzhao 仓 `deployments/README.md`。

## 快速开始（部署态）

```sh
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
curl http://127.0.0.1:18081/healthz                # 宿主直验需临时映射；栈内验证：
docker compose -f compose.prod.yaml exec apiserver wget -qO- http://127.0.0.1:18081/healthz
```

## 与 zhuzhao 的接线（唯一两件事）

1. **网络**：本栈 `zhuzhao_to_activelist`（external）与本栈 apiserver 别名 `activelist`——zhuzhao app 容器须加入同一网络（其 `deployments/docker-compose.yaml` 已默认挂接）。
2. **密钥对值**：`ACTIVELIST_CALLER_ZHUZHAO_SK` = zhuzhao 侧 `GATEWAY_SK`（HMAC 出站签名验签）。zhuzhao 网关默认 target `http://activelist:18081`，无需改配置。

## 运维要点

- **扩缩容**：`docker compose -f compose.prod.yaml up -d --scale apiserver=N`（无状态；共享网别名天然负载到全部副本）。
- **备份**：pgbackup 每日 02:00 `pg_dump -Fc` 保留 14 份 + WAL 归档（`archive_timeout=300s`）——恢复步骤见 [backup/README.md](backup/README.md)。
- **健康**：`/healthz`（存活）/`/readyz`（就绪，检 PG）；compose healthcheck 已接。
- **已知边界（触发驱动挂起项）**：无 metrics 端点（观测=结构化日志+健康探针，ES 聚合线触发时补）；容器日志落容器内 `logs/` 未挂卷（重建即丢，聚合前可接受）。


## 文档 revision 迁移（000002，2026-11-05）

升级前先完成数据库备份，并停止全部旧 apiserver 写实例；旧代码仍按单行+1或导入置1分配版本，不能与新代码混跑。构建/部署新版本时由启动迁移统一锁定存量动态表，将持久revision序列初始化到所有存量version与当前全库事务号之上；后者保护已清空旧表仍被客户端保留的历史version（旧API每事务对同一行至多递增一次）。全新无类型库没有历史数据版本，序列从1开始。迁移完成后再开放新实例流量。迁移000002只修改默认值和后续分配方式，保留现存行数据与version。

`version`仍为数值字段，后续创建、更新、软删、恢复、导入分配全库新revision；空导入和ID重用不会重启revision。客户端须使用响应/重读的version，不自行+1。备份/恢复须包含sequence状态；禁止手工重置revision序列。回滚须停止新写实例并同批回退代码与000002数据库变更，不能单独删除sequence后继续运行新程序。

该保证限于同一数据库连续运行及排空旧写者的正常原地升级。手工历史改版、逻辑恢复导致事务历史丢失、恢复到旧备份或down/up重新建序列不能保证与所有旧客户端缓存隔离；这些操作须先隔离写入口并要求客户端重读，不能沿用旧revision盲写。本迁移没有跨数据库的外置版本世代。
