# 开发与验证

从仓库根目录执行以下命令。分档入口依赖 Git、Python 3 与 Make；ordinary 产品检查另需 Go 1.27.4（版本由 `go.mod` 指定）。需要数据库或 Docker 的测试在独立测试环境运行。

```sh
make hooks
make quality-plan                 # 先审阅文件范围、档位与计划
make quality-check                # 文档/针对检查；ordinary提示手工完整门禁后退出
# 基线不是 origin/main 时显式指定：make quality-check BASE=<本地已存在的基线ref>
```

`make test-integration` 使用 testcontainers 的真实 PostgreSQL 18.6；需要可用 Docker。`make fmt` 会改源码格式，文档整理不需要运行它。
预期为命令退出码 0；门禁通过只证明该次指定环境，不表示候选已合并或部署。

## 分档门禁

`make quality-plan`只输出计划，`make quality-check`执行同源分类及document/reference检查；ordinary档退出并指向指南中的原门禁，须在独占环境手工执行，不自动连接服务或把打印清单当验证通过。两者默认`BASE=origin/main`，对基线到HEAD的完整差异加暂存、未暂存、未跟踪文件分类，避免只看末次提交。正式push/PR由事件精确SHA窗口分类；base缺失强制ordinary并披露无法历史比较，异常计划或候选不一致拒绝，不猜测成文档豁免。

- `document`：纯说明、设计草稿、报告与导航，仅跑文档/本地链接检查及始终启用的提交规范；不声称运行产品测试。
- `reference`：候选Schema、规划JSON、已隔离参考或工具，增加受影响文件的Schema/JSON/工具语法与专属验证；不能据此认定产品集成已通过。
- `ordinary`：业务代码、运行配置、依赖、迁移、正式协议、产品测试、CI、分类器、门禁/豁免规则及未知文件，运行完整适用产品门禁；混合改动取最高档。门禁改动自身也在此档，测试先行纪律保持。

分类器以仓内明确路径规则判定，文件扩展名本身不豁免。`document-check`始终执行；`reference-check`按计划执行；原`test` job只在`runtime=true`执行。最终`quality-gate`以`always()`汇总实际job结果：应跑项缺失、失败、取消或skipped均拒绝，计划外产品job的skipped须明确。必需检查策略是否已在远端启用需另核，不能从本地配置推断。

完整产品门禁保留以下既有命令与环境要求：

- `make lint`、`make test`、`make build`；CI保留`go vet ./...`、gofmt、`go test -race ./...`。
- `go test -tags integration -race ./...`使用testcontainers独占PG；原`make test-integration`入口保留。本地保留Ryuk清理，CI的临时runner保留既有`TESTCONTAINERS_RYUK_DISABLED=true`。
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test -tags integration ./...`；`CGO_ENABLED=0 go build -trimpath -o <临时目录>/apiserver ./cmd/apiserver`后运行同版本`govulncheck -mode=binary <临时目录>/apiserver`。

## 宿主启动与验证

开发形态是 PG 容器加宿主 apiserver，不是生产双副本 Compose。`deploy/compose.dev.yaml` 只启动 PG，并把容器5432映射到宿主15432；启动服务时由应用执行迁移。先按主仓[开发依赖与宿主联调](https://github.com/tracerbiubiubiu/zhuzhao/blob/phase5/docs/ops/deployment.md)核对已有容器、端口和数据归属。

从本仓根目录执行：

```sh
# 仅用于全新隔离栈或已按升级方案迁移的开发栈；旧 deploy_pgdata 保留，不直接复用
# 本模板固定容器名/15432端口；确认旧栈已停并无端口冲突后再启动PG18的新卷
docker compose -p deploy -f deploy/compose.dev.yaml up -d --wait postgres
make build
# 先在本机终端注入 ACTIVELIST_CALLER_ZHUZHAO_SK，与主仓 GATEWAY_SK 对值
: "${ACTIVELIST_CALLER_ZHUZHAO_SK:?请先注入本环境调用方 SK}"
./bin/apiserver serve configs/config.yaml
```

已分配其它开发栈时，先核实其 PG host/port/user/password/dbname、容器名和卷再设置 `ACTIVELIST_PG_*`；仅改 `-p` 不能绕过该文件的固定容器名/宿主15432，也可能指向另一只卷。已有数据库沿用原口令，不因文档示例而重建卷或改密码。生产使用 [部署指南](../ops/deployment.md) 的独立配置，不能沿用开发口令。

`cmd/apiserver/main.go` 默认读取 `configs/config.yaml`；显式路径用法如上。配置项见 [配置与容量](../ops/configuration.md)，其中 `ACTIVELIST_HTTP_PORT` 控制监听端口。正常结果是迁移/装配成功、服务保持运行。另开终端对实际分配端口验证；默认18081时：

```sh
curl --fail http://127.0.0.1:18081/healthz
curl --fail http://127.0.0.1:18081/readyz
```

两者预期 HTTP200，ready 只检查 PG。接着经主仓网关验证类型列表等业务链，确认签名、路由与授权配置；直连业务 API 没有 AK/SK 会401。退出宿主进程用其终端的 Ctrl-C，保留 PG 卷和日志。

## 提交规范


遵 zhuzhao `docs/standards.md` §12-9 / §12-10；关联主仓[复核报告的 RR-GIT-01](https://github.com/tracerbiubiubiu/zhuzhao/blob/phase5/docs/review/reports/zhuzhao-report-followup-2026-11-08.md)（2026-11-08）。
本仓脚本与主仓同源、自包含，仅依赖 Git + Python 3 标准库，不依赖主仓目录或新增 Go/npm 包。
主题为 `type(scope): 摘要`（scope 可省略），摘要去前缀后最多 50 个 Unicode 字符、不得以句号结尾；
主题与正文间须空行，正文不得为空或只有空白。所有新 merge 提交也须显式提供规范主题与正文。
修复正文仍按公约填写「改动摘要 / 影响面 / 验证证据」与同类扫描，硬检查①–⑤不替代人工验证证据。

```sh
make hooks              # 新 clone / 新工作树开工时安装
make commit-lint        # origin/当前分支..HEAD；初推无同名远端时从启用基线检查
make test-commit-lint    # 临时独立仓库黑盒反例，不触碰业务栈
# 显式历史审计（完整 SHA；不豁免历史）
python3 scripts/commit-policy.py range --before <完整SHA> --after <完整SHA> --strict
```

`make hooks` 启用 Git 的 worktree 配置，仅在当前工作树设置相对 `.githooks`；common
`core.hooksPath` 保留，尚未承接本批的 main/其它工作树不被改装。保留在途显影与跨主题纯重题
amend 保护，`commit-msg` 先挡消息违规，`pre-push` 按 Git 提供的每个 ref 新旧 SHA 检查完整窗口。
`GIT_INDEX_FILE` 保持原值，兼容显式 pathspec 提交，不暂存或重写其它会话文件。

固定启用基线见 `scripts/commit-policy-baseline`：仅其祖先为已推送存量，不改史、不自动前移。
CI push 使用事件 before/after，PR 使用真实 base/head SHA，初推与 force push 均有覆盖；
缺对象精确获取仍失败、非法 SHA 或浅克隆一律拒绝，不回退近 20 笔。产品测试命令保留，按本页分档计划运行。
