# activelist

**简体中文 | [English](README.en.md)**

**用 Schema 定义清单，通过 HTTP 管理数据。**

activelist 是基于 PostgreSQL 的动态清单服务。为联系人、映射表、规则清单等结构化数据注册类型后，即可使用统一的校验、查询、更新和导入导出接口，减少重复开发简单数据管理服务。

产品族：[zhuzhao](https://github.com/tracerbiubiubiu/zhuzhao) · [zhuzhao-ui](https://github.com/tracerbiubiubiu/zhuzhao-ui) · **activelist** · [taskrunner](https://github.com/tracerbiubiubiu/taskrunner) · [zhuzhao-utils](https://github.com/tracerbiubiubiu/zhuzhao-utils)

在 zhuzhao 中，它承载动态清单；也可以作为由可信应用调用的独立 HTTP 服务。

## 当前能力

- **运行时定义类型**：支持 `int`、`string`、`int_list`、`string_list`，可配置必填、长度、列表项数和标量组合唯一键。
- **Schema 演进**：类型版本检查、变更历史与废弃；类型元数据、动态表及历史在 PostgreSQL 事务内提交。
- **数据管理**：创建、读取、局部更新、乐观并发控制、软删除和恢复。
- **查询与交换**：游标分页、关键词／标量字段筛选、流式 JSON 导出及事务内全量替换导入。
- **服务接入**：AK/SK HMAC 验签、结构化日志、存活与就绪探针。

当前实现为一个 apiserver，PostgreSQL 中每个类型对应动态表，业务字段存入 JSONB。字段、revision 和导入语义见 [数据契约](docs/api/data-contract.md)。

## 本地上手

需要当前 [go.mod](go.mod) 要求的 Go、Make、Python 3、Docker Compose v2 和 curl。以下用于新建开发环境，默认使用宿主端口 **15432**（PG）和 **18081**（API）；Compose 固定容器名为 `activelist-dev-postgres`，已有实例请先按 [开发指南](docs/guides/development.md)核对资源归属。

从仓库根目录执行：

```sh
docker compose -p activelist-demo -f deploy/compose.dev.yaml up -d --wait postgres

# 生成本次演示密钥；接入已有网关时应改用与网关匹配的密钥
export ACTIVELIST_CALLER_ZHUZHAO_SK="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
export ACTIVELIST_PG_PORT=15432
export ACTIVELIST_HTTP_PORT=18081
make build
./bin/apiserver serve configs/config.yaml
```

服务启动时应用迁移。另开终端检查，正常返回 HTTP 200：

```sh
curl --fail http://127.0.0.1:18081/healthz
curl --fail http://127.0.0.1:18081/readyz
```

业务接口需要签名。调用方 AK 为 `zhuzhao`，SK 为上面注入的值；可用 [zhuzhao-utils 的签名客户端](https://github.com/tracerbiubiubiu/zhuzhao-utils/blob/main/docs/guides/usage.md)接入。一个类型注册请求 `POST /api/v1/admin/types` 的 body 如下，随后可向 `/api/v1/data/security_contacts` 写入或查询数据：

```json
{
  "type_name": "security_contacts",
  "fields": [
    {"name": "team", "type": "string", "required": true},
    {"name": "owner", "type": "string", "required": true}
  ],
  "unique_fields": ["team"]
}
```

完整请求与响应见 [HTTP API](docs/api/contract.md)。探针可直连；未经签名的业务请求返回 401。开发 Compose 中的口令仅用于本地；本项目可丢弃测试库允许按归属重建。需要保留数据的部署升级才按[部署与恢复指南](docs/ops/deployment.md)保护原卷，不把测试重建当存量迁移。

## 适用范围

activelist 适合有限结构的清单和映射数据。CMDB、风险、身份和工单等业务正本仍由各自系统管理。

当前服务签名校验的是调用方，`X-Operator` 用于操作者归因。zhuzhao 网关提供登录与接口权限检查；逐用户的类型、行、字段权限及统一能力调用属于 [平台集成计划](docs/design/platform-integration.md)，尚未完成。`sensitive` 字段标记也不是字段访问控制或密码托管能力。

导入是**全量替换**，不是追加或 upsert。当前接口没有任意查询／聚合语言。AI、MCP 和可靠变更订阅请按主仓路线及对应实施状态阅读。

## 文档与贡献

- [文档导航](docs/README.md)：从当前实现、设计或运维入口继续。
- [配置与容量](docs/ops/configuration.md) · [部署](docs/ops/deployment.md) · [备份恢复](docs/ops/backup.md)。
- [开发指南](docs/guides/development.md) · [协作协议](AGENTS.md)：测试先行，小范围 PR，保留失败与验证证据。

```sh
make hooks
make lint
make test
make build
make test-integration  # Docker + testcontainers 的真实 PostgreSQL 测试
```

工具链与容器升级的候选状态见 [版本升级记录](docs/ops/version-upgrade.md)；当前检出代码、main 与实际部署是不同状态。

欢迎通过 [Issues](https://github.com/tracerbiubiubiu/activelist/issues)提交具体场景或复现步骤，通过 PR 改进实现和文档。

## 许可

[MIT](LICENSE)。
