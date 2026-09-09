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
# 1. 停写（apiserver 缩容或断网）
docker compose stop apiserver

# 2. 清库重建（或 drop/create 目标库）
docker compose exec postgres psql -U activelist -d postgres \
  -c "DROP DATABASE activelist;" -c "CREATE DATABASE activelist OWNER activelist;"

# 3. 恢复指定备份
cat backups/al-20260909.dump | docker compose exec -T postgres \
  pg_restore -U activelist -d activelist --no-owner --role=activelist

# 4. 启动并抽验（/readyz + 名单数据计数）
docker compose start apiserver
```

## WAL 归档（PITR，可选进阶）

`wal_archive` 卷保存 5 分钟粒度 WAL 段。基于某份 base backup 做 PITR：

1. 取一份 `al-*.dump` 全量恢复至临时实例（见上）；
2. 临时实例 `recovery.signal` + `postgresql.auto.conf` 配 `restore_command = 'cp /wal_archive/%f %p'`
   与 `recovery_target_time`；
3. 演练/接管后重置归档起点。

> PITR 流程**未实测**（依赖真实部署环境演练）——首次演练安排在部署批，本节为操作蓝本。

## 注意

- 备份失败**不静默**：pgbackup 日志可见（`docker compose logs pgbackup`），脚本保留现场次日重试；
- `backups` / `wal_archive` 卷建议纳入宿主机级外部备份（卷快照/同步），防单机盘损；
- 保留期调整：`RETAIN_COUNT`（份数）/ `RETAIN_DAYS`（脚本兼容字段）。
