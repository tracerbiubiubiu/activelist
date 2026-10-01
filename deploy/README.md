# activelist 部署 runbook

> 部署态 = `compose.prod.yaml`（本目录）；开发态 = `compose.dev.yaml`（仅 PG，服务跑宿主 `go run ./cmd/apiserver`@8080）。
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
curl http://127.0.0.1:8080/healthz                # 宿主直验需临时映射；栈内验证：
docker compose -f compose.prod.yaml exec apiserver wget -qO- http://127.0.0.1:8080/healthz
```

## 与 zhuzhao 的接线（唯一两件事）

1. **网络**：本栈 `zhuzhao_to_activelist`（external）与本栈 apiserver 别名 `activelist`——zhuzhao app 容器须加入同一网络（其 `deployments/docker-compose.yaml` 已默认挂接）。
2. **密钥对值**：`ACTIVELIST_CALLER_ZHUZHAO_SK` = zhuzhao 侧 `GATEWAY_SK`（HMAC 出站签名验签）。zhuzhao 网关默认 target `http://activelist:8080`，无需改配置。

## 运维要点

- **扩缩容**：`docker compose -f compose.prod.yaml up -d --scale apiserver=N`（无状态；共享网别名天然负载到全部副本）。
- **备份**：pgbackup 每日 02:00 `pg_dump -Fc` 保留 14 份 + WAL 归档（`archive_timeout=300s`）——恢复步骤见 [backup/README.md](backup/README.md)。
- **健康**：`/healthz`（存活）/`/readyz`（就绪，检 PG）；compose healthcheck 已接。
- **已知边界（触发驱动挂起项）**：无 metrics 端点（观测=结构化日志+健康探针，ES 聚合线触发时补）；容器日志落容器内 `logs/` 未挂卷（重建即丢，聚合前可接受）。
