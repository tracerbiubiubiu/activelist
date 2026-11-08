# activelist

独立PostgreSQL动态数据模型服务：类型注册、Schema演进、四类动态字段校验、数据CRUD、软删恢复及JSON导入导出。用户权限在zhuzhao网关，服务间使用AK/SK签名。

从 [文档导航](docs/README.md) 开始。架构见 [当前设计](docs/design/overview.md)，接入查 [HTTP](docs/api/contract.md) 与 [数据契约](docs/api/data-contract.md)，运维查 [部署](docs/ops/deployment.md)、[配置](docs/ops/configuration.md)、[备份恢复](docs/ops/backup.md)。开发查 [验证与提交](docs/guides/development.md)。

当前为单apiserver、PG每类型表+JSONB。原MongoDB三进程/事件架构完整保留在历史归档，不是现行合同；候选修复不表示已合main或部署。

许可：[MIT](LICENSE)。
