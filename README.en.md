# activelist

**[简体中文](README.md) | English**

**Define a list with a schema. Manage its data over HTTP.**

activelist is a PostgreSQL-backed service for dynamic structured lists. Register a type for contacts, mappings or rule lists, then use shared validation, query, update and import/export APIs instead of building another small data service.

Project family: [zhuzhao](https://github.com/tracerbiubiubiu/zhuzhao) · [zhuzhao-ui](https://github.com/tracerbiubiubiu/zhuzhao-ui) · **activelist** · [taskrunner](https://github.com/tracerbiubiubiu/taskrunner) · [zhuzhao-utils](https://github.com/tracerbiubiubiu/zhuzhao-utils)

It provides dynamic lists for zhuzhao and can also run as a standalone HTTP service called by a trusted application.

## Available today

- **Runtime type definitions:** `int`, `string`, `int_list` and `string_list`, with required fields, length/item limits and composite uniqueness over scalar fields.
- **Schema evolution:** type revision checks, change history and deprecation. Metadata, dynamic tables and history commit in a PostgreSQL transaction.
- **Data management:** create, read, partial update, optimistic concurrency control, soft delete and restore.
- **Queries and exchange:** cursor pagination, keyword/scalar-field filters, streaming JSON export and transactional full-replacement import.
- **Service integration:** AK/SK HMAC authentication, structured logs, liveness and readiness probes.

The current implementation has one apiserver and one PostgreSQL table per type, with application fields stored in JSONB. See the [data contract](docs/api/data-contract.md) for fields, revisions and import semantics.

## Run locally

Install the Go version required by [go.mod](go.mod), Make, Python 3, Docker Compose v2 and curl. This example creates a fresh development environment using host ports **15432** for PG and **18081** for the API. The Compose file fixes the container name to `activelist-dev-postgres`; check existing resources using the [development guide](docs/guides/development.md) before reusing a machine or database.

From the repository root:

```sh
docker compose -p activelist-demo -f deploy/compose.dev.yaml up -d --wait postgres

# Generate a demo key. An existing gateway must use a matching key.
export ACTIVELIST_CALLER_ZHUZHAO_SK="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
export ACTIVELIST_PG_PORT=15432
export ACTIVELIST_HTTP_PORT=18081
make build
./bin/apiserver serve configs/config.yaml
```

The application applies migrations at startup. In another terminal, both checks should return HTTP 200:

```sh
curl --fail http://127.0.0.1:18081/healthz
curl --fail http://127.0.0.1:18081/readyz
```

Business requests require a signature. Use caller AK `zhuzhao` and the SK injected above; the [zhuzhao-utils signing client](https://github.com/tracerbiubiubiu/zhuzhao-utils/blob/main/docs/guides/usage.md) provides the client transport. For example, send this body to `POST /api/v1/admin/types`, then write or query records through `/api/v1/data/security_contacts`:

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

See the [HTTP API](docs/api/contract.md) for full requests and responses. Probes are public; unsigned business requests return 401. Development credentials are for local use. Disposable project test databases may be recreated after checking resource ownership. For deployments whose data must be preserved, follow the [deployment and recovery guide](docs/ops/deployment.md) and protect the original volumes; a clean test reset is not a data migration.

## Scope

activelist suits bounded lists and mappings. CMDB, risk, identity and ticket records remain owned by their respective systems.

Service signatures authenticate the caller; `X-Operator` records attribution. The zhuzhao gateway checks login and route permissions. Per-user type, row and field authorization and the common capability path are [planned integration work](docs/design/platform-integration.md), not completed features. The `sensitive` field flag is not field access control or credential storage.

Import performs **full replacement**, not append or upsert. The current API has no arbitrary query/aggregation language. AI, MCP and durable change subscriptions follow the main project's roadmap and implementation status.

## Documentation and contributing

- [Documentation](docs/README.md): current implementation, design and operations.
- [Configuration and capacity](docs/ops/configuration.md) · [Deployment](docs/ops/deployment.md) · [Backup and recovery](docs/ops/backup.md).
- [Development guide](docs/guides/development.md) · [Collaboration rules](AGENTS.md): tests first, focused PRs and explicit validation evidence.

```sh
make hooks
make lint
make test
make build
make test-integration  # Real PostgreSQL tests using Docker and testcontainers
```

See [version upgrades](docs/ops/version-upgrade.md) for toolchain and container candidates. A checked-out candidate, main and an actual deployment are distinct states.

Share concrete use cases or reproductions in [Issues](https://github.com/tracerbiubiubiu/activelist/issues), or contribute a focused PR.

## License

[MIT](LICENSE).
