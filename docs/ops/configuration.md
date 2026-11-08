# 配置与容量

权威字段为 `internal/config/config.go` 的 `Config` / `Load` 及根目录 `configs/config.yaml`，不是历史Mongo配置草图。yaml支持 `${VAR:-default}` 环境替换，ACTIVELIST_*可由环境注入。以下说明实际旋钮，不复制密钥值。

- server.port缺省18081，read_timeout缺省30s，write_timeout缺省300s；上传、流式验签与导入总耗时必须配套，不能只调字节上限。
- postgres为host/port/user/password/dbname/max_open_conns等分字段，DSN由utils构造。宿主开发端口15432，compose内5432。开发PG口令缺省不代表生产可用；生产compose要求显式PG口令和调用方SK。
- business.page_size_default=20、page_size_max=100；import_batch_rows=1000仅控制同一个事务内批大小，不把导入拆成多个提交；import_max_bytes缺省1GiB。
- security.callers为AK→SK密钥环，空环或空条目拒启。无需self出站身份，当前服务没有主动调用zhuzhao的链路。生产compose没有dev-gateway-sk弱缺省。
- log.level/dir控制技术日志，sensitive标记当前不触发服务端脱敏；容器日志目录目前未挂卷，重建丢失的边界保留。

导入临时文件按并发×上限准备磁盘，默认系统临时目录，可用TMPDIR指向已有可写挂载；每请求0600。成功、拒绝、业务失败和panic展开清理；SIGKILL/宿主故障残留由临时目录清理策略处理。底层时间窗仍±5min，不能靠更大write_timeout绕过认证时间限制。

所有副本共享PG，副本数×连接池上限要留出迁移/备份等连接余量。事务内已取得连接后必须沿tx查询，防MaxConns=1死锁。import和普通写使用事务级lock_timeout=5s；postgres.statement_timeout若更短，可能先触发其他错误，不能简单宣称永远409。

启动迁移由应用执行并用advisory lock保护；升级000002的停写和备份要求见 [部署](deployment.md)。恢复与PITR的逻辑/物理区别见 [备份](backup.md)。
