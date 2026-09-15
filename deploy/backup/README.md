# activelist 备份与恢复（M-A6 部署件）

## 策略

| 层 | 机制 | 频率 | 保留 |
|---|------|------|------|
| 逻辑备份 | `pg_dump -Fc`（pgbackup 服务，每日 02:00） | 每日 | 14 份（`RETAIN_COUNT` 可调） |
| 增量 | WAL 归档（postgres `archive_mode=on`，`archive_timeout=300s`） | 持续 | 随 `wal_archive` 卷 |
| 数据安全底线 | 乐观锁 + 软删 + 导入事务（应用层，M-A3/A5） | — | — |

备份卷：`backups`（pg_dump 产物）、`wal_archive`（WAL 归档，pgbackup 只读挂载）。

## 恢复步骤（pg_dump → 全量恢复）

```sh
# 均在 deploy/ 目录执行（compose.prod.yaml 不在 docker compose 自动发现列表，
# 显式 -f 避免「名字没对上就不动」的静默空转）
# 1. 停写（apiserver 缩容或断网）
docker compose -f compose.prod.yaml stop apiserver

# 2. 清库重建（或 drop/create 目标库）
docker compose -f compose.prod.yaml exec postgres psql -U activelist -d postgres \
  -c "DROP DATABASE activelist;" -c "CREATE DATABASE activelist OWNER activelist;"

# 3. 恢复指定备份
cat backups/al-20260909.dump | docker compose -f compose.prod.yaml exec -T postgres \
  pg_restore -U activelist -d activelist --no-owner --role=activelist

# 4. 启动并抽验（/readyz + 名单数据计数）
docker compose -f compose.prod.yaml start apiserver
```

## WAL 归档（PITR，可选进阶）

> **⚠ 2026-09-14 实测勘误（原蓝本作废）**：PITR 的基准备份必须是**物理基备（`pg_basebackup`）**——
> `al-*.dump` 是 pg_dump **逻辑导出**，恢复出的集群 LSN 与原库 WAL 时间线不接续，
> **无法用归档 WAL 回放**。原「dump 恢复 + restore_command 回放」蓝本不可行。
> 实测过的完整做法（zhuzhao 侧已跑通：`deployments/backup/README.md`）：

```sh
# 1. 物理基备（容器内落盘再 docker cp 出来；tar 流出 stdout 模式强制带流 WAL 不可用）
docker exec activelist-postgres-1 sh -c 'pg_basebackup -D /tmp/base -Ft -U activelist'
docker cp activelist-postgres-1:/tmp/base /tmp/al_base_dir

# 2. 解包至卷（注意卷根=数据目录，勿多套一层；recovery.signal 必须手建；chown 归属运行用户）
docker run --rm -v <基备卷>:/data -v /tmp/al_base_dir:/host:ro alpine sh -c \
  'mkdir -p /data/pg_wal && tar -xf /host/base.tar -C /data && tar -xf /host/pg_wal.tar -C /data/pg_wal \
   && touch /data/recovery.signal && chown -R 999:999 /data'

# 3. 临时实例回放（挂同一 wal_archive 卷；目标时刻=误操作前一刻）
docker run -d --name al-pitr -v <基备卷>:/var/lib/postgresql/data \
  -v activelist_wal_archive:/wal_archive:ro postgres:15-alpine \
  postgres -c restore_command='cp /wal_archive/%f %p' \
           -c recovery_target_time='<目标时刻>' -c recovery_target_action='promote'

# 4. 若报「recovery ended before configured recovery target was reached」：
#    目标时刻所在的当前部分段尚未归档——在活库 pg_switch_wal() 强制切段后重启临时实例即可
```

> 实测结果（zhuzhao 侧）：回放精确停在 recovery_target_time 前最后一条提交，
> 目标时刻前的行在、后的行不在。

## 注意

- 备份失败**不静默**：pgbackup 日志可见（`docker compose -f compose.prod.yaml logs pgbackup`）；失败当日每 10 分钟自动重试、失败日不做轮转（防把好备份轮掉）、成功才标记当日完成；
- **常见失败根因**：postgres 未运行（pg_dump 报 `could not translate host name "postgres"`）——postgres 已配 `restart: unless-stopped` 自愈；若手工 `docker compose -f compose.prod.yaml stop postgres` 停库，备份会持续重试失败直至库恢复，属预期行为；
- `backups` / `wal_archive` 卷建议纳入宿主机级外部备份（卷快照/同步），防单机盘损；
- 保留期调整：`RETAIN_COUNT`（份数，默认 14）。
