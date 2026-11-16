# activelist 文档导航

> **用途**：按任务选择阅读入口。**设计状态**：现行PG薄层与后续集成目标分列。**实施状态**：以准确源码/版本和运行回执为准，目标合同不表示R06或互通增量已实现。

## 当前实现与接口

- [当前设计](design/overview.md)：单apiserver、PG每类型表和方案D；先理解数据写主及现有限制。
- [HTTP](api/contract.md)与[数据/校验](api/data-contract.md)：现行字段、revision、patch和全量replace语义；调用前核消费者版本。

## 目标设计与计划

- [设计导航](design/README.md)区分当前解释与候选合同；[平台集成合同](design/platform-integration.md)承接R06及AL受权写、源事实/订阅等新增目标。
- 排期、设计成熟度和新增范围只读主仓[唯一总账](../../zhuzhao/docs/planning/planning-backlog.md)及[互通增量](../../zhuzhao/docs/planning/capability-interconnect-supplement.md)，不在本仓复制预算。

## 开发、运行与验证

- [开发指南](guides/README.md)：工具、测试先行、验证命令和提交；协作入口见[AGENTS](../AGENTS.md)。
- [运维入口](ops/README.md)：配置、部署、升级和备份恢复。浏览器网关1MiB导入与服务端授权运维大导入通道分别核验，不以探针健康替代业务链验证。

## 决策与历史

[ADR](adr/README.md)保留决策及后继关系；[正式报告与历史](review/README.md)按冻结时点追溯。原MongoDB三进程方案不作为现行接口或实施指令，旧门禁收据也不证明新候选已验证。

## 统一入口与Git保留范围

全项目阅读从主仓[文档总入口](../../zhuzhao/docs/README.md)开始，当前计划统一在[planning](../../zhuzhao/docs/planning/README.md)；本仓继续维护随源码版本变化的合同、指南和正式结论。原始调研、执行卡、原日志、截图、备份与纯操作收据归仓库外项目档案，短期执行产物可留ignored `.codex/`；材料ID与保留决定见[材料目录](../../zhuzhao/docs/materials.md)。机器台账、冻结来源、Schema样例和必要验证依据继续Git跟踪，不按扩展名批量忽略。

复用审计方法、空白模板及开发提示词见主仓[工作资料库](../../zhuzhao/docs/toolkit/README.md)；创建和收尾按[文档维护约定](../../zhuzhao/docs/documentation.md)执行，原件与执行实例分开保存。
