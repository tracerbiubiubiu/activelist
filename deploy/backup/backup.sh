#!/bin/sh
# activelist 每日物理备份循环（M-A6 部署件）：
#   - 每日 02:00 后执行一次 pg_dump -Fc（自定义压缩格式）
#   - 保留最近 RETAIN_DAYS 份，超出轮转删除（文件名含日期，按名排序即按时间序）
#   - WAL 归档由 postgres 服务 archive_command 持续写入 /wal_archive（只读挂载于此）
# 环境变量（compose 注入）：PGHOST/PGUSER/PGPASSWORD/PGDATABASE/BACKUP_DIR/RETAIN_DAYS
# 恢复步骤见同目录 README.md。
set -eu

BACKUP_DIR="${BACKUP_DIR:-/backups}"
RETAIN_DAYS="${RETAIN_DAYS:-14}"
RETAIN_COUNT="${RETAIN_COUNT:-14}"

mkdir -p "$BACKUP_DIR"

do_backup() {
  stamp="$(date +%Y%m%d)"
  target="$BACKUP_DIR/al-$stamp.dump"
  if [ -f "$target" ]; then
    echo "[backup] $target 已存在，跳过"
    return 0
  fi
  echo "[backup] start $target"
  pg_dump -Fc -f "$target"
  echo "[backup] done $(ls -lh "$target" | awk '{print $5}')"
}

rotate() {
  # 按文件名倒序保留前 RETAIN_COUNT 份，其余删除
  ls -1 "$BACKUP_DIR"/al-*.dump 2>/dev/null | sort -r | tail -n +$((RETAIN_COUNT + 1)) | while IFS= read -r f; do
    echo "[backup] 轮转删除旧备份: $f"
    rm -f "$f"
  done
}

last=""
while :; do
  today="$(date +%F)"
  # 每日 02:00 后、当日未备份 → 执行一次；其后每 10 分钟轮询
  if [ "$(date +%H)" -ge 2 ] && [ "$last" != "$today" ]; then
    do_backup || echo "[backup] 备份失败（保留现场，次日重试）"
    rotate
    last="$today"
  fi
  sleep 600
done
