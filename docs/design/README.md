# activelist设计导航

> **用途**：区分实现解释与后续合同。**设计状态**：当前模型有实现依据，集成增强为目标候选。**实施状态**：源事实/订阅与统一调用等新增机制未实现，T0未启动。

## 当前实现解释

[overview](overview.md)说明单进程PG薄层、方案D、数据归属和限制；准确字段与协议转[API](../api/README.md)。现有CRUD/CAS/import不等于完整受权任务或变化触发链。

## 目标设计与实施窗口

[平台集成合同](platform-integration.md)承接R06/P7的并发唯一、schema、授权查询、导入恢复；另补AL写主最小变更凭证、批次/manifest、主仓订阅与受控任务写入目标。后者新增范围按主仓[互通增量](../../../zhuzhao/docs/review/capability-interconnect-supplement.md)单列复估，不能计入旧R06原价。

统一ID及调用语义以[主仓能力调用合同](../../../zhuzhao/docs/design/capability-invocation-contract.md)为权威；排期、依赖和状态只回[唯一总账](../../../zhuzhao/docs/review/planning-backlog.md)，本页不维护第二份进度。

## 运行验证与历史

实际部署、备份及故障证据从[运维](../ops/README.md)进入；[ADR](../adr/README.md)与[历史](../review/README.md)保留原MongoDB及职责收敛的时点含义。“AL不感知事件”的旧表述已由后继源事实设计限域校准，不能据旧方案宣称新机制存在或应被取消。
