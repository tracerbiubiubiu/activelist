# 配置与容量

权威字段为 `internal/config/config.go` 的 `Config` / `Load` 及根目录 `configs/config.yaml`，不是历史Mongo配置草图。yaml支持 `${VAR:-default}` 环境替换，ACTIVELIST_*可由环境注入。以下说明实际旋钮，不复制密钥值。

- server.port缺省18081，read_timeout缺省30s，write_timeout缺省300s；上传、流式验签与导入总耗时必须配套，不能只调字节上限。
- postgres为host/port/user/password/dbname/max_open_conns等分字段，DSN由utils构造。宿主开发端口15432，compose内5432。开发PG口令缺省不代表生产可用；生产compose要求显式PG口令和调用方SK。
- business.page_size_default=20、page_size_max=100；import_batch_rows=1000仅控制同一个事务内批大小，不把导入拆成多个提交；import_max_bytes缺省1GiB。
- security.callers为AK→SK密钥环，空环或空条目拒启。普通 CRUD 不需要出站身份。生产compose没有dev-gateway-sk弱缺省。
- log.level/dir控制技术日志，sensitive标记当前不触发服务端脱敏；容器日志目录目前未挂卷，重建丢失的边界保留。

导入临时文件按并发×上限准备磁盘，默认系统临时目录，可用TMPDIR指向已有可写挂载；每请求0600。成功、拒绝、业务失败和panic展开清理；SIGKILL/宿主故障残留由临时目录清理策略处理。底层时间窗仍±5min，不能靠更大write_timeout绕过认证时间限制。

所有副本共享PG，副本数×连接池上限要留出迁移/备份等连接余量。事务内已取得连接后必须沿tx查询，防MaxConns=1死锁。import和普通写使用事务级lock_timeout=5s；postgres.statement_timeout若更短，可能先触发其他错误，不能简单宣称永远409。

启动迁移由应用执行并用advisory lock保护；升级000002的停写和备份要求见 [部署](deployment.md)。恢复与PITR的逻辑/物理区别见 [备份](backup.md)。


## B3 单类型接线

`ACTIVELIST_B3_TYPE_NAME` 缺省为空。启用前，在隔离库、尚未启用 B3 时注册所选类型。首次启用前，停止接入并排空所有使用该库的 ActiveList 实例，包括运行新版本但尚未启用 B3 的实例。确认这些实例全部退出后，再以相同受审配置启动实例并恢复接入。已运行的实例仅在启动时核对流配置，混用未启用实例可能绕过该类型的变更出箱。配置通过以下环境变量绑定，变量均以 `ACTIVELIST_B3_` 开头。

- `TYPE_NAME`、`ANCHOR_FIELD`、`ANCHOR_VALUE` 固定类型和行范围。锚点须为必填、非敏感字符串，直接受信插入由 AL 设置锚点。现有记录须有匹配锚点才进入本次受控范围。
- `WRITE_FIELDS`、`READ_FIELDS`、`EVENT_FIELDS` 为逗号分隔的非敏感标量名单。锚点不在名单内。写和读各最多 32 项，事件投影最多 8 项。
- `PRODUCER_AK`、`PRODUCER_SK` 只用于受监控类型的直接插入和更新。`CORE_AK`、`CORE_SK` 只用于固定的主仓内部命令。独立身份须使用独立密钥。
- `GRANT_REF`、`GRANT_REVISION` 固定更新准入的授权引用和版本。`READ_GRANT_REF`、`READ_GRANT_REVISION` 固定只读准入。可用 `SUBJECT_ID` 和 `READ_SUBJECT_ID` 约束对应主体。
- `PUSH_URL` 是固定 Core 端点，路径为 `/internal/activelist/changes/push`。`PUSH_AK`、`PUSH_SK` 为 AL 出站身份。`CONSUMER_ID` 与 Core 收件配置一致。

已启用的流与类型的数据库 ID、锚点、字段名单、消费者和授权引用绑定。缺少配置或更换绑定内容时应用拒启。迁移 `000003_b3_change_source` 保存源事实和游标，`000004_b3_operation_receipts` 保存更新回执。存在已启用流时，两条迁移都拒绝直接回退。通过备份保留流、待投递事实及已确认凭证，恢复使用同一配置。积压超过 1024 条会拒绝新的有变化写入。投递失联时优先恢复原收件端，发送器从最早未确认游标重投原事件，后续游标等待确认。
