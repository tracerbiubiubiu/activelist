# 版本升级记录（Phase6 开工前）

Go 1.26.10 → 1.27.4；builder 为 golang:1.27.4-alpine3.24，runtime 从 Alpine3.20 →3.24.4。PG15 →18.6-trixie；开发/部署Compose、备份客户端与三个testcontainers夹具一致。

本记录对应 2026-11-12 的独立维护候选，业务能力、API、运行时配置字段与数据库 schema 不变；保留 mock 接口，不启动 Phase6 业务功能。本地验证与后续 PR/部署分开记账。

GOFLAGS/GOMAXPROCS两个可选构建参数仅作用于builder，不进入运行层。构建器、运行镜像和 PG/Redis 均固定完整版本与 OCI 多架构摘要；后续更新需重新核官方摘要并跑相应门禁。CI 使用 checkout v7.0.1 / setup-go v7.0.0 的提交 SHA（Node24，托管 ubuntu-latest）；自托管 runner 至少 2.327.1。当前 checkout 的源代码以 govulncheck v1.9.0 扫描；服务另构建当前二进制扫描，不引用其它仓未推送提交。漏洞库会更新，扫描结论仅对当次环境有效；源码可达性与二进制符号扫描边界不同。

依赖按用途逐项更新：pgx 5.10→5.13（响应校验、batch认证与重试安全修复）；migrate4.19.1→4.20.1（Postgres驱动、安全依赖和迁移索引优化）；testcontainers0.44→0.45，postgres模块同步，采用修复后的等待/OCI处理；crypto/sys/protobuf及必需MVS间接依赖同步。testcontainers此版对其它数据库/身份示例模块有破坏性变更，本仓只用Postgres Run/Container，不引入这些适配器。compress固定1.20.2，修复GO-2026-6865在testcontainers归档工具链中的保守可达调用；CI源扫描包含-test与integration标签，使用默认text的真实失败退出码，不靠JSON退出0判无漏洞。未新增业务依赖或数据库迁移。pgx新增BatchResults.Tx，仓内仅测试fake需补齐nil事务；集成PG夹具清理使用独立有界context并检查失败，避免误用已cancel的启动context。夹具同时等待PG双就绪日志与宿主映射端口（上游 `modules/postgres/wait_strategies.go` 的 `BasicWaitStrategies` 要求），保留原日志等待上限，避免Mac/Windows端口代理尚未就绪就取ConnectionString。

PG18挂载父目录与独立pgdata18、wal_archive18、backups18见 [部署](deployment.md)。旧PG15卷不能直接换镜像挂载，新卷初始化不等于数据迁移。PITR示例显式设置独立恢复路径PGDATA=/restore，避免依赖不同PG主版本的镜像默认目录；恢复仍必须使用与物理基备/WAL相同主版本。

公共库仍使用原已推 pin（cd759a040245）；通过MVS升级底层依赖，不引入本机replace或未推SHA。

官方依据：[Go发布](https://go.dev/dl/)、[pgx5.13](https://github.com/jackc/pgx/blob/v5.13.0/CHANGELOG.md)、[migrate4.20.1](https://github.com/golang-migrate/migrate/releases/tag/v4.20.1)、[testcontainers0.45](https://github.com/testcontainers/testcontainers-go/releases/tag/v0.45.0)、[PG18镜像说明](https://hub.docker.com/_/postgres)。

仓内装配仍采用现有手工同步的 `wire_gen.go`；没有新增Wire生成器依赖，不把未配置的生成过程列为已验证。

## 验证状态

最终候选（compress1.20.2及端口守卫）完成Go1.27.4 `make lint`、`make test`、`make build`，全量integration标签race为196个测试/子测试pass事件、0skip。扩展到 `-test -tags integration` 的govulncheck1.9默认text扫描明确No vulnerabilities found、退出0。原候选发现GO-2026-6865并退出3的记录保留，不能只扫生产源码而漏测试工具链。

真实Dockerfile的linux/arm64构建成功，ELF包含Go1.27.4构建信息、运行底座Alpine3.24.4；非root日志目录探针通过，提取ELF的默认text漏洞扫描通过。安全依赖只进入测试工具链，重构建后生产ELF与之前交付产物逐字节相同（SHA256 `6e747cc90a0690f7486026f6af620820492b5e67ce29177bbb0ec4ae52adc93c`）；不靠“理论不影响”复用旧运行证据。

PG18默认官方entrypoint和全新归档卷，精确 `cp %p /wal_archive/%f` 已在独立恢复实例验证：实际切WAL后archived_count=1、failed_count=0，16MiB文件摘要有收据；未添加无实证的权限补丁。这只证明该组合能归档，完整物理基备＋连续WAL的PITR仍为G05首次生产前恢复门禁。两仓备份脚本在PG18 Debian镜像中通过sh语法检查，pg_dump/pg_restore版本均18.6；注释统一为逻辑备份。

原始port映射未就绪失败、测试漏洞命中、资源压力与审计helper错误均保留，不以最终通过覆盖原失败。完整模块目录中的withdrawn、已修复版本和未用AWS/旧PGX/grpc/Snowflake/daemon依赖分开登记；已确认当前生产调用图无这些漏洞包。未编译目录库存按G05每月补丁调查（下一次2026-12）与季度支持复核，新增驱动前重新评审，不扩展本服务的数据库能力。

日志、候选输入摘要、旧/新OCI及ELF逐字节比对保留在本轮主仓忽略目录 `.codex/audits/version-modernization-20261112/go-siblings/`，精确AL归档回执位于同次审计的data目录。本地未运行amd64应用产物；远端CI、生产部署与存量共享开发栈切换本轮未执行，旧卷和旧实例保留。未重算覆盖率，不将这些本地收据写成新发布版本。
