# HTTP API

路由入口 `internal/handler/server.go` 的 `New`，数据绑定与游标解析在 `internal/handler/data.go`。前缀 `/api/v1`，业务路由统一AK/SK验签；用户权限在zhuzhao网关。探针 `/healthz` 与 `/readyz` 免鉴权，ready检PostgreSQL。

| 方法与路径 | 契约 |
|---|---|
| `POST /admin/types` | body type_name/fields/unique_fields，注册当前定义；同名409。 |
| `GET /admin/types` | 类型列表。 |
| `GET /admin/types/rules` | 与服务端校验同源的命名/字段类型/长度规则。 |
| `GET /admin/types/:typeName` | 当前完整定义和metadata version。 |
| `POST /admin/types/schema` | body type_name/fields/unique_fields/version，完整定义替换；CAS冲突409。 |
| `POST /admin/types/deprecate` | body type_name，幂等废弃、不可逆。 |
| `GET /admin/types/:typeName/history` | 类型Schema历史，新到旧。 |
| `POST /data/:typeName` | body `{data:{...}}`，插入并返回完整文档。 |
| `GET /data/:typeName` | 复合游标分页与筛选，见下。 |
| `GET /data/:typeName/:id` | id单查，软删行仍可见。 |
| `POST /data/:typeName/:id/update` | body `{data:{...},version:数字}`，合并后全量校验并CAS更新。 |
| `POST /data/:typeName/:id/delete` | 幂等软删，返回删除后完整文档；body可选，空body/{}维持旧契约，带正整数version时行锁内核验，不匹配409零写。 |
| `POST /data/restore` | body type_name/id（字符串），可选version语义同delete；幂等恢复，版本拦截先于活跃唯一键冲突检查。 |
| `GET /data/:typeName/export` | 含软删行的裸JSON数组，文件不走信封。 |
| `POST /data/:typeName/import` | body为单个裸JSON数组，全量替换，不是追加/upsert；数组闭合后仅允许尾随空白，多余内容按400拒绝、事务回滚（CMP-R02）。 |

表中路径均接 `/api/v1`。保留资源型data路径为现有契约，文档整理不改路由。类型schema/deprecate与restore的旧POST路径已退役，不按Mongo原稿接入。

## 分页、过滤和响应

列表使用after_created_at（RFC3339）+after_id成对游标，按created_at DESC/id DESC，page_size默认20/上限100（可配置）。返回data含list/page_size/next_cursor，无total，不支持offset跳页；满页给下一游标，下一页空则next_cursor=null。游标中的时区+须URL编码，id与after_id按字符串处理。

include_deleted只认true/1，缺省仅active。q搜索全部标量字段的文本表示，ILIKE包含且转义通配符；field+value必须成对，字段需存在且为int/string，列表/未知字段422。q当前长度检查按Go字符串字节长度执行上限100；错误文案写“字符”，多字节输入以实际校验为准。查询支持范围以 `DataService.List` 为准，无任意排序和聚合接口。

普通成功采用utils信封 `{code:0,message,data,request_id}` 与HTTP200；失败保留400/401/404/409/413/422/500/503等具体语义。原本地msg/detail.error_code信封已经被替代。错误文案对调用方隐藏服务端诊断，字段校验上下文经message呈现；技术细节在服务日志。业务码映射见 `internal/apperr` 和 `internal/handler/response.go` 的 `Fail`。

## 大导入边界

普通CRUD最大16MiB内存验签，import使用utils GinStreamingMiddleware按business.import_max_bytes有界暂存私有文件。读失败400、超限413、鉴权401、服务器暂存故障503；认证成功才rewind进入事务。默认直连服务上限1GiB且受read/write超时与HMAC时间窗约束。浏览器经zhuzhao网关仍1MiB，大导入是授权运维通道；不得在普通上传页宣称可传1GiB。

字段类型、unique_fields第一档限制、revision与导入替换风险见 [数据契约](data-contract.md)。

## B3 受控活动清单命令

单类型 B3 启用后，直接生产者仅可在原数据接口写入受监控类型。插入沿用 `{"data":{...}}` 并由 AL 设置行锚点，更新沿用 `data` 与 `version`。两条路径仅接受获准字段，响应的 `data` 只返回锚点和可写字段。普通调用方不能用旧数据接口查询、写入、软删、恢复、导入或导出受监控类型。

多入口使用相同的行拒绝边界。不存在的文档和超出授权范围的文档返回相同的 404。类型内的唯一性约束继续生效，受监控类型发生冲突时返回通用 409。旧类型列表的结果与总数都排除已登记 B3 流的类型，旧详情和历史按同一规则拒绝。B3 的固定读取命令仍按准入返回获准字段。

以下 POST 路由均只接受专用 Core AK/SK 签名，使用原有 AL 响应信封。请求只接受签入 body 的操作、目标准入和字段，不从 URL query 取业务参数。请求的准入有效期最多 30 秒。AL 在事务内复核行锚点和类型世代。

- `/internal/activelist/documents/update` 接收已绑定 CAS 的 `al_execution_request`。返回类型、文档 ID、世代、已提交 revision 和 `noop`。同操作同输入返回原受权回执，同号异参或版本冲突返回 409。同值操作保存回执并保留原 revision。
- `/internal/activelist/documents/get` 接收 `al_read_request`。返回指定字段的投影、查询时间和当前 revision。读取不改变文档或游标。
- `/internal/activelist/documents/receipt` 查询原更新操作的回执。Core 按当前授权重新发出短时查询准入。查到原操作则返回原回执，缺少原记录时返回 404。查询不提交新写入。

数据或世代冲突、目标不可见、授权失效分别保留各自的 HTTP 语义。持久源事实和 ACK 的推送接线见 [B3 单类型接线](../ops/configuration.md#b3-单类型接线)。
