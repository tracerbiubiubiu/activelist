# 当前设计

[overview](overview.md)说明单进程PG薄层、方案D、数据归属和未实施能力。[平台集成合同](platform-integration.md)承接 R06/P7 的并发唯一、schema、IAM查询、导入恢复与专业平台边界，排期归主仓唯一总账。字段及协议按 [API](../api/README.md)；原MongoDB方案仅在历史归档。

2026-11-13设计补充：[平台集成合同](platform-integration.md)将AL写主变更凭证、批次/manifest、主仓统一订阅及受控任务写入纳入目标；不再用旧“AL不感知事件”推导源事实可以缺失。统一ID及调用语义回链[主仓能力调用合同](../../../zhuzhao/docs/design/capability-invocation-contract.md)。本轮仍是设计快照，T0未启动；新增范围在P7单列复估，原预算和当前API不变。
