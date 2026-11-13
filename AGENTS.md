# AGENTS.md — activelist协作入口

适用于本仓全部文件和每次AI/人工协作。长规则只维护权威来源，本页不另立一套公约。

## 职责与权威

- 本仓是独立PG动态清单薄服务，拥有类型/schema和数据写主；新增源事实按目标合同实施，用户授权/业务策略归主仓，任务调度归TR。
- [文档导航](docs/README.md)区分现行实现、目标设计、运行验证与历史；字段/接口以本仓[API](docs/api/README.md)和实际源码为准。
- 全生态工程、API、安全、迁移与测试纪律遵[主仓standards](../zhuzhao/docs/standards.md)；新需求/状态/估算只回填[唯一总账](../zhuzhao/docs/review/planning-backlog.md)。
- [平台集成合同](docs/design/platform-integration.md)含未实施目标，不能将源事件/订阅、受控任务写入等设计当成当前已有能力。

## 开发与验证

- 开工先`make hooks`，核对分支、工作树、在途文件及数据库/端口归属；写任务独立worktree，测试使用专属环境。
- 保留standards §11测试先行：当前批准批全量正/负/边界/权限用例先写，取得目标行为的红证据，再分批实现到绿。
- 修复必带回归；并发修复遵§9/§12移除修复后连续至少8次稳定变红，不以`-race`全绿替代；红证据不等于允许提交红测试。
- 现有命令从本仓根目录运行：`make lint`、`make test`、`make build`；PG集成另跑`make test-integration`（需Docker）。
- 数据库/schema/导入变更须验证实际PG事务、并发及恢复；mock/contract/real分列，真实未验保持待验。
- 完整适用门禁未绿不标完成；失败、重试、skip及NOT_RUN如实记录，不删除断言或放宽预期制造绿灯。

## 交付与边界

- 交付固定三节：改动摘要、影响面、验证证据；沿用[短审阅卡](../zhuzhao/docs/phase6/implementation-handoff.md#单维护者短审阅卡)，同步受影响合同/模块和主仓控制台。
- 仅提交本批明确路径，使用`git commit -F <消息文件> -- <paths>`等显式路径形态；禁`git add -A`、`git commit -a`和裸提交。
- 提交前核归属，提交后核`git show --stat`；全绿后提交，不擅自amend/reset/rebase他人历史。
- 合入走已确认的PR/单维护者策略及实际生效规则；不因文档存在宣称远端保护已启用。默认不push、合main、发版或切共享环境。
- 不跨库读写主仓/TR，不向AL移入IAM/编排或CMDB权威；私有连接器走公开合同，不能依赖其他仓internal。
- 文档、代码、mock均不得引入公司真实名称、人员、接口、地址或凭据；合成样例与私有adapter隔离，秘密不入Git/日志。
- 不改仓库/module/API/配置/卷名以追求一致外观；结构迁移须先核真实消费者与数据恢复边界。

操作前置与失败排查见[开发指南](docs/guides/development.md)和[运维入口](docs/ops/README.md)；未知外部条件如实列出，不自行补成事实。
