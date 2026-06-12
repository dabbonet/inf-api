# WARP 上游笔记

这些笔记跟踪了针对 WARP 开源客户端验证的事实：
`warpdotdev/warp@1c2d4cc` 以及该客户端使用的固定 `warp-proto-apis` 修订版。

## 认证存储

稳定的 Windows 应用将持久化的用户存储在：

`%LOCALAPPDATA%\warp\Warp\data\dev.warp.Warp-User`

该文件使用 Windows DPAPI 加密。解密后，刷新令牌存储在 `id_token.refresh_token` 中。顶层的 `refresh_token` 可能是遗留的或空的。

## 多代理传输

客户端将 protobuf 请求发布到：

`https://app.warp.dev/ai/multi-agent`

响应为 `text/event-stream`；每个 SSE `data:` 有效载荷都是用于 `warp.multi_agent.v1.ResponseEvent` 的 base64-url-safe protobuf。

## 协议源

WARP 当前固定在：

`https://github.com/warpdotdev/warp-proto-apis.git@c67de64fc4949f693a679552dc88cebc9f7d0180`

公开的 `warpdotdev/warp` 仓库当前指向相同的 proto 修订版，并包含使用多代理 API 的 Rust 应用程序源代码。最相关的上游比较点是：

- `app/src/ai/agent/api/impl.rs`
- `app/src/server/server_api.rs`
- `app/src/server/server_api/ai.rs`
- `crates/graphql/src/api/queries/get_feature_model_choices.rs`
- `crates/warp_graphql_schema/api/schema.graphql`

有用的文件位于 `apis/multi_agent/v1` 下：

- `request.proto`
- `response.proto`
- `task.proto`

生成的 Go 包可以通过 `github.com/warpdotdev/warp-proto-apis/apis/multi_agent/v1/gen/go` 导入。生产环境现在直接编组这个官方请求类型；之前捕获的十六进制/字节模板构建器已被删除。

## 借用的行为

- 首选嵌套的持久化令牌，如 `id_token.refresh_token`。
- 匹配官方 WARP 标头中的客户端版本和操作系统元数据。
- 抑制 `User-Agent` 并依赖于 `X-Warp-Client-ID`，匹配 Warp 自定义客户端角色标头行为。
- 解析 SSE base64 protobuf 响应事件。
- 保持后备工具字段编号与 `task.proto` 对齐。

## 当前请求形状

Orchids-2api 现在导入固定的 `warp-proto-apis` 修订版使用的相同生成的 Go proto 模块，并编组结构化的 `warp.multi_agent.v1.Request`，而不是修补捕获的十六进制模板。

当前请求构建反映了上游的 `api::Request` 形状：

- `task_context` 存在，且任务列表为空。
- `input.context` 包括目录、操作系统、shell 和当前时间戳。
- `input.type = user_inputs`。
- `user_inputs.inputs[].user_query.query` 包含当前用户轮次。
- 处理程序记录请求构建器编组的相同的 `UserQuery.query` 预览，而不是生成单独的本地 Warp 提示。
- `metadata.conversation_id` 仅针对服务器发出的 Warp 会话 ID 填充，而不是本地 `chat_*` 占位符。
- `settings.model_config.base` 是请求的模型。
- `settings.model_config.cli_agent = "cli-agent-auto"`。
- `settings.model_config.computer_use_agent = "computer-use-agent-auto"`。
- `settings.model_config.coding` 留空，因为官方 Warp 不再发送此已弃用的角色字段。
- `settings.model_config.base_model_context_window_limit = 0`。
- `settings.supports_parallel_tool_calls = true`
- `settings.supports_reasoning_message = true`
- `settings.web_search_enabled = true`
- `settings.supports_v4a_file_diffs = true`
- 当启用工具时，`settings.supported_tools` 遵循上游本地会话对于 shell、文件、grep、MCP、子代理、文档、提示建议、应用差异和搜索代码库工具的默认设置。
- `settings.supported_cli_agent_tools` 遵循上游本地会话默认设置。
- `mcp_context.servers` 将请求声明的工具分组在 Orchids 服务器下。

## 模型发现和可用性

官方 Warp 运行在一个已登录用户的模型配置下。Orchids-2api 运行多账号池，因此跨账号聚合的模型列表可能会暴露选定账号实际无法调用的模型。

实时验证表明 GraphQL 可见性并不等同于可调用性：`workspace.availableLlms(includeAllConfigurableLlms: true)` 可能返回 Claude/Gemini 变体等模型，这些模型稍后会在 `/ai/multi-agent` 失败，并提示 `the requested base model (...) is not allowed for your account` 或 `No model available` 等错误。

上游行为：

- 使用 `GetFeatureModelChoices` 获取特定功能的模型选项。
- 使用 `workspaces[].featureModelChoice.agentMode` 获取代理模式可调用的选项。
- `workspace.availableLlms(includeAllConfigurableLlms: true)` 仍然是一个宽泛的目录表面，而不是当前的代理模式路由源。

当前行为：

- 首选 `workspaces[].featureModelChoice.agentMode.choices` 作为受信任的可调用模型来源。
- 保持 `auto-open` 为默认 Warp 模型。
- 将旧的 auto 别名（`auto`、`auto-efficient`、`auto-genius`）映射到 `auto-open`。
- 使用 `auto-open` 重试 Warp HTTP 400 模型可用性失败一次。
- 将 Warp 模型可用性错误与通用客户端 400 错误分离开来，以便重试/账号切换策略可以将它们视为账号/模型可用性问题，而不是客户端输入格式错误。

## 已知差异

- 官方 `RequestParams` 可以发送单独的编码、CLI 代理、计算机使用、BYO 密钥、自定义提供商、研究代理、捆绑技能和编排设置。Orchids-2api 当前使用官方默认角色模型，并禁用这些可选的高级设置。
- 官方客户端将本机 Warp 会话状态转换为单独的 `UserInputs` 和操作结果输入。Orchids-2api 接收 OpenAI/Claude 风格的聊天记录，因此它将当前的用户/工具结果轮次桥接到一个 `UserQuery.query` 中，直到实现完整的操作结果映射器。
- 官方 `ResponseEvent.StreamFinished.should_refresh_model_config` 告诉客户端其模型配置何时过时。我们现在解析并记录此信号，但尚未触发自动模型刷新。
- 官方 `StreamFinished` 包含请求成本和会话使用量元数据。我们目前只保留令牌使用量。
- 官方模型刷新绑定到单个用户的当前模型配置。Orchids-2api 保留每个账号的模型选择缓存，因为它在池化的 Warp 账号集上进行路由。

## 建议的后续步骤

更安全的下一步是实施低并发的每个账号模型探测/缓存：

- 仅探测选定/默认的候选模型，而不是完整的可配置目录。
- 缓存 `(account_id, model_id) -> allowed/unavailable`，设置较短的 TTL。
- 在账号选择期间使用缓存，以便针对特定模型的请求选择已知允许该模型的账号。
- 通过调度序列化的 Warp 刷新并使相关的允许性缓存失效来消耗 `should_refresh_model_config`。
- 当我们希望在多轮对话上与 Warp 本都会话状态有更紧密的奇偶校验时，添加从传入的工具结果消息到官方 `Request.Input.UserInputs.UserInput.ToolCallResult` 的完整映射器。
