# activelist 备份与恢复（M-A6 部署件）

## 策略

| 层 | 机制 | 频率 | 保留 |
|---|------|------|------|
| 逻辑备份 | `pg_dump -Fc`（pgbackup 服务，每日 02:00） | 每日 | 14 份（`RETAIN_COUNT` 可调） |
| 增量 | WAL 归档（postgres `archive_mode=on`，`archive_timeout=300s`） | 持续 | 随 `wal_archive` 卷 |
| 数据安全底线 | 乐观锁 + 软删 + 导入事务（应用层，M-A3/A5） | — | — |

备份卷：`backups`（pg_dump 产物）、`wal_archive`（WAL 归档，pgbackup 只读挂载）。

## 恢复步骤（pg_dump → 全量恢复）

先核对恢复目标的 Compose 项目、`.env`、PG 版本与卷归属，并保留恢复前备份；清库命令只用于已经确认可覆盖的恢复目标。下面从仓库根进入 `deploy/`，每一步成功后再继续，不把恢复失败当作可以开写的状态。`compose.prod.yaml` 不在自动发现列表，始终显式 `-f`。

```sh
cd deploy
# 1. 从实际服务定位已完成备份，不选择 .part 临时文件
docker compose -f compose.prod.yaml exec -T pgbackup sh -c 'ls -lh /backups/al-*.dump'
backup_file=al-YYYYMMDD.dump  # 替换为上一步确认的实际文件名
restore_dir="$PWD/../.codex/restore-check"
mkdir -p "$restore_dir"
docker compose -f compose.prod.yaml cp "pgbackup:/backups/$backup_file" "$restore_dir/$backup_file"
test -s "$restore_dir/$backup_file"
docker compose -f compose.prod.yaml exec -T postgres pg_restore --list \
  < "$restore_dir/$backup_file" > "$restore_dir/$backup_file.list"

# 2. 检查备份日期/对象清单；确认该目标可重建后停全部写实例与备份循环
docker compose -f compose.prod.yaml stop apiserver pgbackup
docker compose -f compose.prod.yaml exec -T postgres psql -U activelist -d postgres \
  -v ON_ERROR_STOP=1 -c "DROP DATABASE activelist;" -c "CREATE DATABASE activelist OWNER activelist;"

# 3. 恢复归档；失败时保持停写，不执行下一步
docker compose -f compose.prod.yaml exec -T postgres \
  pg_restore -U activelist -d activelist --no-owner --role=activelist --exit-on-error \
  < "$restore_dir/$backup_file"

# 4. 恢复无错后启动并抽验探针、类型/数据计数和revision，再恢复备份循环
docker compose -f compose.prod.yaml start apiserver
docker compose -f compose.prod.yaml exec -T --index 1 apiserver wget -qO- http://127.0.0.1:18081/readyz
docker compose -f compose.prod.yaml start pgbackup
```

备份在 named volume 的 `/backups`，不会自动出现在宿主 `deploy/backups/`；`compose cp` 按实际项目服务取文件，无需猜卷名。`pg_restore --list` 只确认归档可解析并提供对象清单，不代替恢复后的数据核验；清单和实际恢复均应包含 `_al_document_revision_seq` 及其状态。旧备份恢复后先隔离旧客户端缓存、要求重读，避免使用恢复前revision盲写。

## WAL 归档（PITR，可选进阶）

> **⚠ 2026-09-14 实测勘误（原蓝本作废）**：PITR 的基准备份必须是**物理基备（`pg_basebackup`）**——
> `al-*.dump` 是 pg_dump **逻辑导出**，恢复出的集群 LSN 与原库 WAL 时间线不接续，
> **无法用归档 WAL 回放**。原「dump 恢复 + restore_command 回放」蓝本不可行。
> 实测方法的来源和边界见主仓[备份与恢复](https://github.com/tracerbiubiubiu/zhuzhao/blob/phase5/docs/ops/backup.md)；下列为需按现场容器、卷名和目标时间替换的演练示例：

本节命令从本仓根目录先 `cd deploy` 后执行，沿用该恢复目标的 `.env` 与已确认 Compose 项目。PITR 需要事故前已完成的物理基备及从基备到目标时刻的连续 WAL。下面的基备命令只说明如何提前准备，事故后新做基备不能倒退恢复到它之前；已有基备的实际恢复应跳过第1步，把 `base_dir` 设为已校验的物理基备目录。以下示意需在独占演练/恢复环境逐步执行；先设置 `AL_PITR_VOLUME` 为可新建的专属空卷、`AL_PITR_NAME` 为独占容器名、`AL_RECOVERY_TARGET_TIME` 为已核对时区的目标时间。不得把原 `pgdata` 当作解包目标。

先从物理基备的 `PG_VERSION` 确认 PostgreSQL 主版本（tar 包先用 `tar -tf base.tar` 定位该文件），再选择同主版本的恢复镜像。下文从源实例读取 `pg_image` 仅适用于已经核对版本相符的情况；恢复较早基备时，当前源实例可能已经升级，不能默认它的现行镜像仍适用。源容器元数据已丢失时，应从备份台账恢复镜像和 WAL 来源信息，不能猜测替代。

```sh
# 1. 提前准备物理基备；容器内先落盘，再按服务复制到本机忽略目录
#    al-base-YYYYMMDD 必须替换为本次尚未使用的目录名
pitr_base=/tmp/al-base-YYYYMMDD
docker compose -f compose.prod.yaml exec -T postgres pg_basebackup -D "$pitr_base" -Ft -U activelist
mkdir -p ../.codex
base_dir=$(mktemp -d "$PWD/../.codex/al-pitr-base.XXXXXX")
docker compose -f compose.prod.yaml cp "postgres:$pitr_base/." "$base_dir"

# 2. 从已确认的源实例读取真实WAL卷和镜像，不猜项目名、容器名或运行用户UID
pg_container=$(docker compose -f compose.prod.yaml ps -a -q postgres)
test -n "$pg_container"
wal_volume=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/wal_archive"}}{{.Name}}{{end}}{{end}}' "$pg_container")
pg_image=$(docker inspect --format '{{.Image}}' "$pg_container")
test -n "$wal_volume"
: "${AL_PITR_VOLUME:?请先指定独占空恢复卷}"
: "${AL_PITR_NAME:?请先指定独占恢复容器名}"
: "${AL_RECOVERY_TARGET_TIME:?请先指定目标时间及其时区}"

# 3. 只向空卷解包；使用与源PG相同镜像中的postgres用户归属，不硬编码UID
docker run --rm --user 0 --entrypoint sh \
  -v "$AL_PITR_VOLUME:/restore" -v "$base_dir:/base:ro" "$pg_image" -ec '
    test -z "$(ls -A /restore)"
    mkdir -p /restore/pg_wal
    tar -xf /base/base.tar -C /restore
    tar -xf /base/pg_wal.tar -C /restore/pg_wal
    touch /restore/recovery.signal
    chown -R "$(id -u postgres):$(id -g postgres)" /restore
    chmod 700 /restore
  '

# 4. 在独立、无网络的临时实例回放，原WAL卷只读；核验前不切换应用目标
docker run -d --name "$AL_PITR_NAME" --network none \
  -v "$AL_PITR_VOLUME:/var/lib/postgresql/data" -v "$wal_volume:/wal_archive:ro" "$pg_image" \
  postgres -c restore_command='cp /wal_archive/%f %p' \
    -c "recovery_target_time=$AL_RECOVERY_TARGET_TIME" -c recovery_target_action=promote
docker logs "$AL_PITR_NAME"
```

若目标时间位于尚未归档的当前部分段，可能报 `recovery ended before configured recovery target was reached`。先由维护者确认源端是否需 `pg_switch_wal()`、归档是否成功及WAL是否连续，再重试专属恢复实例；不能只改目标时间凑成功。目标卷应保留以供核验，恢复实例完成不等于已接管应用；切换前核对目标时刻前后数据、revision与旧客户端缓存隔离。

> 实测结果（zhuzhao 侧）：回放精确停在 recovery_target_time 前最后一条提交，
> 目标时刻前的行在、后的行不在。

## 注意

- 备份失败**不静默**：pgbackup 日志可见（`docker compose -f compose.prod.yaml logs pgbackup`）；失败当日每 10 分钟自动重试、失败日不做轮转（防把好备份轮掉）、成功才标记当日完成；
- **常见失败根因**：postgres 未运行（pg_dump 报 `could not translate host name "postgres"`）——postgres 已配 `restart: unless-stopped` 自愈；若手工 `docker compose -f compose.prod.yaml stop postgres` 停库，备份会持续重试失败直至库恢复，属预期行为；
- `backups` / `wal_archive` 卷建议纳入宿主机级外部备份（卷快照/同步），防单机盘损；
- 保留期调整：`RETAIN_COUNT`（份数，默认 14）。


恢复前核对实际compose项目名、backups/wal_archive卷名与文件；文中容器/卷名是演练示例，不替代现场盘点。revision序列状态必须在备份中，恢复旧备份后隔离旧客户端缓存并重读，见 [部署升级](deployment.md)。
