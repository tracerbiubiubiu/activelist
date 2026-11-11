# activelist 文档

当前权威模型是单apiserver的PG薄层，不是历史MongoDB三进程方案。

1. 先读 [当前设计](design/overview.md)，理解职责与方案D。
2. 接入按 [HTTP](api/contract.md) 和 [数据/校验](api/data-contract.md)，重点确认revision、全量替换与应用查重边界。
3. 后续 R06/P7 的数据安全、IAM和智能体查询边界看 [平台集成合同](design/platform-integration.md)，排期引用主仓唯一总账。
4. 开发看 [指南](guides/README.md)，部署、升级、备份看 [运维](ops/README.md)。
5. 追溯 [ADR](adr/README.md) 与 [正式时点报告/历史](review/README.md)。

源码整合与实际部署分别核对，规划文档不代表 R06 已实现。浏览器网关导入1MiB，与服务端授权运维大导入通道分开。
