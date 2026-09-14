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
  # 先落临时文件、成功后原子改名：失败/中断不残留 0 字节假备份
  tmp="$BACKUP_DIR/.al-$stamp.dump.part"
  if ! pg_dump -Fc -f "$tmp"; then
    echo "[backup] pg_dump 失败，删除残留临时文件"
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$target"
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
  # 每日 02:00 后、当日未备份 → 执行一次；其后每 10 分钟轮询。
  # 仅【成功】才标记当日已完成——失败日保持未标记，10 分钟后自动重试，
  # 且失败时不做轮转（防止把最近的好备份轮掉、只留空文件）。
  if [ "$(date +%H)" -ge 2 ] && [ "$last" != "$today" ]; then
    if do_backup; then
      rotate
      last="$today"
    else
      echo "[backup] 备份失败，10 分钟后重试"
    fi
  fi
  sleep 600
done
