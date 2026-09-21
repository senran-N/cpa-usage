# cpa-usage

CLIProxyAPI (CPA) 的使用量统计插件。以 C-ABI 动态库形式加载，记录经由 CPA 转发的每一次模型请求，估算费用，并提供一个内嵌的 Web 看板与一组 JSON 查询接口。

## 功能范围

- **用量采集**：订阅 CPA 的 `usage.handle` 事件，落库保存 provider、模型、客户端 API Key、上游凭证、会话 ID、延迟、TTFT、失败状态码与各类 token 计数。
- **费用估算**：依据内置价格表估算每次请求的美元成本，区分 input / output / cache read / cache creation 四类计价。
- **动态价格中心**：支持通过 LiteLLM、OpenRouter、models.dev 或自定义 JSON URL 在线同步最新模型费率，支持用户对任意模型自定义价格覆盖（Custom Override）与持久化存储。
- **敏感数据脱敏**：落库与返回的错误响应体、诊断信息经过全自动凭据脱敏清洗，防止 Bearer Token、API Key、PEM 私钥意外泄露。
- **账号健康与配额追踪**：基于滑动窗口对上游账号进行 100 分制动态健康评分，智能识别正常、限流冷却、配额耗尽、凭据失效（需重登）等状态；实时解析 Codex 5H 与周级额度使用率及恢复时间。
- **稳定性与错误诊断**：聚合统计系统请求失败率、HTTP 状态码分布、错误类型分布，自动提取结构化错误摘要并关联上游 Trace ID。
- **数据迁移与备份**：支持 CSV、JSONL、JSON 多格式条件导出，支持无缝导入并智能去重 CPA-Manager-Plus 历史数据及本插件数据备份。
- **持久化**：使用纯 Go 的 SQLite 实现（`modernc.org/sqlite`），无需 CGO 以外的额外依赖。写入经由内存队列批量提交。
- **查询接口**：按时间、模型、provider、API Key、凭证、成功/失败等维度聚合与分页查询，并提供丰富的运维与管理 API。
- **Web 看板**：单页面应用，HTML/CSS/JS 通过 `//go:embed` 内嵌在动态库中，不依赖外部 CDN；具备响应式现代化界面、账号健康概览、配额进度条、价格管理与诊断中心等交互面板。

## 环境要求

| 项目 | 要求 |
|---|---|
| Go | 1.26 或更高（见 `go.mod`） |
| CGO | 必须启用，需要可用的 C 编译器（GCC / Clang / Zig CC） |
| CLIProxyAPI | v7.3.9 或兼容版本，插件契约 schema version 6 |
| 平台 | Windows / Linux / macOS |

## 构建

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o cpa_usage.dll .    # Windows
```

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o cpa_usage.so .     # Linux
```

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o cpa_usage.dylib .  # macOS
```

或使用 `make build`，它会根据当前平台选择后缀。

## 部署

将产物复制到 CPA 的插件目录，文件名需与插件 ID 一致：

```text
CLIProxyAPI/
├── config.yaml
└── plugins/
    └── cpa-usage.dll        # 或 cpa-usage.so / cpa-usage.dylib
```

在 CPA 的 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-usage:
      enabled: true
      db_path: "data/cpa_usage.db"
      retention_days: 90
      # 可选，默认 false。见下方“安全注意事项”。
      unauthenticated_api: false
```

### 配置项

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `db_path` | string | `data/cpa_usage.db` | SQLite 数据库路径，相对于 CPA 的工作目录。父目录会自动创建。 |
| `retention_days` | int | `90` | 保留天数。大于 0 时，插件在打开数据库后执行一次清理，删除早于该天数的记录；设为 0 表示不自动清理。 |
| `unauthenticated_api` | bool | `false` | 是否把 JSON 接口同时挂到 CPA 的资源路径上。资源路径不经过管理认证，详见下方安全章节。保持默认值时看板改用管理密钥访问 management 路由。 |

`scripts/setup.sh` 与 `scripts/setup.bat` 封装了"复制动态库 + 打补丁"的步骤，假定 `CLIProxyAPI` 与本仓库位于同级目录。

## 访问看板

插件通过 `management.register` 注册资源路由，看板可直接访问：

```text
http://<cpa-host>:<port>/v0/resource/plugins/cpa-usage/dashboard
```

看板以 `X-Management-Key` 头访问管理接口，密钥即登录 CPA 控制面板使用的那个口令。密钥的来源有两种：

- **在控制面板登录时勾选了「记住密码」**：面板会把密钥混淆后存在同源的 `localStorage['cli-proxy-auth']` 下。看板读取并还原它，无需再次输入，也不另存副本，因此改了面板口令后会自动跟随。
- **未勾选**：面板只把密钥保留在内存里，看板无法获取，会弹窗要求输入一次。输入后存到 `localStorage['cpa-usage:management-key']`，此后该浏览器不再询问。

两种情况下密钥都只存在于浏览器本地，不会写入服务端。

CPA 对同一 IP 连续 5 次密钥错误会封禁 30 分钟，因此看板在密钥缺失或被拒绝时会完全停止发送请求（包括自动刷新），并且每次只用一个 `/ping` 请求校验密钥，不会一次性打满失败次数。

`scripts/patch_observe.py` 可选地把看板入口注入到 CPA 官方控制面板的侧边栏"观测"分组中：

```bash
python scripts/patch_observe.py            # 修改本地 management.html
python scripts/patch_observe.py --fetch    # 本地不存在时先下载再修改
python scripts/patch_observe.py --restore  # 从 .bak 还原
```

该脚本通过字符串匹配修改压缩后的 `management.html`，匹配模式与特定构建版本绑定。CPA 升级控制面板后模式可能失效，此时脚本会跳过对应步骤并打印提示，需要重新适配。它还会无条件开启面板的插件路由开关。若不希望修改官方面板文件，可跳过此步骤，直接使用上面的 URL。

## HTTP 接口

插件注册两组路由，指向同一套处理逻辑：

- **Management 路由**，前缀 `/v0/management/usage/`，经过 CPA 的管理认证。看板默认走这一组。
- **资源路由**，前缀 `/v0/resource/plugins/cpa-usage/`，**不经过认证**，且宿主只放行 GET。默认只注册 `/dashboard` 这一个文档路由；JSON 接口只有在 `unauthenticated_api: true` 时才会额外挂到 `/v0/resource/plugins/cpa-usage/api/` 下。

| 端点 | Management 方法 | 说明 |
|---|---|---|
| `summary` | GET | 全局汇总：请求数、成功/失败数、各类 token、总费用、平均延迟 |
| `timeseries` | GET | 按时间分桶的趋势数据，`interval` 取 `minute` / `hour` / `day`（默认 `hour`） |
| `models` | GET | 按模型聚合 |
| `keys` | GET | 按客户端 API Key 聚合 |
| `auths` | GET | 按上游凭证聚合 |
| `records` | GET | 分页明细，`page`（默认 1）、`page_size`（默认 20，上限 100） |
| `filter-options` | GET | 各维度的去重候选值 |
| `cleanup` | POST | 删除历史记录，`days`（默认 90）或 `before`（时间戳 / RFC3339 / `YYYY-MM-DD`）；`days=0` 或 `days=all` 清空全部 |
| `prices` | GET | 获取模型费率列表（内置、同步与自定义覆盖），支持 `search` 与 `source` 筛选 |
| `prices/override` | POST/DELETE | 针对指定模型设置或删除自定义单价覆盖（$ / 1M Tokens） |
| `prices/sync` | POST | 在线同步官方模型价格（支持 `litellm`、`openrouter`、`models.dev` 或自定义 JSON URL） |
| `diagnostics` | GET | 稳定性与错误诊断数据：失败率、HTTP 状态码统计、错误种类分布与最近失败记录 |
| `export` | GET | 数据导出，支持 `format=csv|jsonl|json`，支持携带筛选条件 |
| `import` | POST | 数据导入与去重合入，支持 JSON 数组或 JSONL（兼容 CPA-Manager-Plus 历史数据） |
| `accounts/health`| GET | 账号健康状态评估列表与汇总概览（100 分制打分、限流冷却、需重登状态识别） |
| `accounts/quota` | GET | 账号配额使用率（Codex 5H 与周窗口使用率、重置恢复时间解析） |
| `ping` | GET | 健康检查 |

默认配置下汇总数据的入口是 `/v0/management/usage/summary`，需要 `X-Management-Key`。处理器对 `cleanup` 同时接受 GET、POST 和 DELETE；开启 `unauthenticated_api` 后，由于资源路由只放行 GET，它在该路径下可通过 GET 触发——参见下一节。

通用查询参数：`start_time`、`end_time`、`model`、`provider`、`api_key`、`auth_id`、`failed`、`search`。时间参数接受 Unix 秒/毫秒时间戳、RFC3339 或 `YYYY-MM-DD`。所有时间在服务端按 UTC 存储与分桶。

## 安全注意事项

**CPA 的资源路径不经过管理认证。** 这是插件宿主的设计：`/v0/resource/plugins/<id>/` 下的 GET 请求不走 management 中间件（见上游 `internal/api/server_management.go` 的 `pluginResourceNoRoute`），因为控制面板要用 iframe 加载插件页面，无法附带管理密钥请求头。CPA 自身的认证是按路由组挂载的，不是全局中间件，所以设置了 `secret-key` 并不会覆盖到这条路径。

因此本插件**默认不在资源路径上暴露任何数据接口**：那里只注册看板文档本身，JSON 接口仅存在于经过认证的 `/v0/management/usage/`，由看板携带管理密钥访问。

`unauthenticated_api: true` 会把 JSON 接口重新挂到资源路径上，恢复无需密钥即可打开看板的行为。开启后，任何能访问 CPA 监听地址的客户端都可以：

- 读取 `/v0/resource/plugins/cpa-usage/api/records`，其中包含**明文的客户端 API Key**、上游凭证 ID、会话 ID 以及上游返回的错误响应体；
- 通过 `GET /v0/resource/plugins/cpa-usage/api/cleanup?days=0` 清空整个用量数据库。

只有在 CPA 仅监听 `127.0.0.1` 且不经反向代理对外暴露时才适合开启。注意这两个前提是会变的：把 `host` 改成 `0.0.0.0`、或在前面加一层反代，这个开关就会从"本机可见"变成"对外可见"，而配置本身不会有任何提示。

数据库文件未加密，其内容应按凭证材料的级别保护。

## 费用估算的准确性

看板中的金额是**估算值，不是账单**，与供应商实际计费存在偏差。已知的偏差来源：

- 价格表来自 LiteLLM 的 `model_prices_and_context_window.json` 快照，内嵌在二进制中，不会自动更新。供应商调价后需要重新构建。
- 模型名采用多级模糊匹配（精确匹配 → 前缀清洗 → 系列匹配 → 子串匹配）。未收录的模型会落到同系列的近似价格上，匹配结果记录在 `matched_model` 字段中，可据此核对。
- 完全无法匹配的模型记为 0 成本，而非报错。
- 价格表未给出缓存价时按启发式推导：缓存读取取输入价的 50%，Claude 系列的缓存写入取输入价的 1.25 倍。
- DeepSeek 按内置峰谷逻辑计价：工作日 UTC 01:00–04:00 与 06:00–10:00（即北京时间 09:00–12:00、14:00–18:00）按 2 倍低谷价计，北京时间周末全天低谷。该逻辑与 sub2api 的实现一致；是否完全等同 DeepSeek 官方公布的错峰规则未经核实。
- 该分支在匹配链最前面短路，因此 `prices.json` 中的 deepseek 条目不会被使用。
- 不计入图片、音频、Web 搜索等按次计费项，也不区分 service tier、长上下文阶梯与 5m/1h 两档缓存写入价。

### Token 口径

CPA 按上游协议的原生口径透传 token，并不把它归一后再交给插件——归一结果存在 `Detail.TokenBreakdown` 里，而插件只收到扁平计数。三种口径分别是：

| 上游 | `input_tokens` 含缓存 | `output_tokens` 含推理 | 上报的 `total_tokens` |
|---|---|---|---|
| OpenAI 系 | 含 | 含 | `input + output` |
| Claude Messages | 不含 | 含 | `input + output + cache_read + cache_write` |
| Gemini 系 / Interactions | 含 | 不含 | `input + output + reasoning` |

因此插件不能一刀切地扣缓存或忽略推理。计费前会用上报的 `total_tokens` 与三种形状比对来还原口径，再决定是否从输入中扣除缓存、是否把推理 token 计入输出；缓存或推理为零时三种形状重合，判断结果不影响金额。无法比对时（`total_tokens` 缺失）退回结构性判断：缓存数超过输入数则必然是独立口径，推理数超过输出数同理。

这样做而不是按 provider 名判断，是因为 OpenAI 兼容端点的 provider 名可由运营者自定义，不可靠。

## 数据与保留

记录写入单表 `usage_records`，在 `requested_at_unix`、`model`、`api_key`、`auth_id`、`provider`、`failed` 上建有索引。数据库以 WAL 模式打开，`busy_timeout` 为 5000ms。

写入路径为：`usage.handle` 回调将记录投入容量 5000 的内存队列后立即返回；后台 worker 按 100 条或 250ms 的阈值批量提交。队列满时退化为单条直接写入。**进程非正常退出时，队列中尚未落盘的记录会丢失**；正常 `plugin.shutdown` 会先排空队列再关闭数据库。

`retention_days` 的清理只在数据库打开时执行一次，不是常驻的定时任务。长期运行的实例需要通过 `/cleanup` 接口或重启来触发清理。删除后不会执行 `VACUUM`，文件大小不会立即回落。

## 测试

```bash
go test ./...
```

`integration_test.go` 带有 `//go:build windows` 约束，会加载已编译的 `cpa_usage.dll` 并通过 C-ABI 调用 `plugin.register`、`usage.handle`、`management.register`、`management.handle`。未找到 DLL 时该测试自动跳过。

## 已知限制

- `plugin.reconfigure` 仅在存储尚未初始化时才会应用 `db_path`。运行中修改该配置需要重启 CPA 才能生效。
- 路由匹配基于路径后缀，不是精确匹配。
- 看板界面为简体中文，未做国际化。
- 时间序列按 UTC 分桶，前端不做时区换算。
- 复用面板密钥依赖控制面板当前的 `localStorage` 存储格式（`enc::v1::` 混淆）。面板改版后该路径可能失效，届时看板会退回弹窗输入，不会报错。

## 许可

MIT License。

`pricing/prices.json` 源自 [LiteLLM](https://github.com/BerriAI/litellm) 的 `model_prices_and_context_window.json`（MIT License）。
