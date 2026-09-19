# Codex 292/332 门票

门票功能按账号和模型缓存上游返回的 `x-codex-turn-state`，并在启用时注入后续请求。后台入口为「系统设置 → 网关服务 → Codex 设置」。

使用默认 `target_length: 292` 时，根据账号访问令牌 JWT 中的 `chatgpt_plan_type` 选择长度：

| JWT 套餐 | 目标长度 |
| --- | --- |
| `free`、`plus`、`pro` | 292 |
| `team`、`business`、`self_serve_business_prolite` | 332 |

套餐未知、JWT 无法解析，或 JWT 中的账号与所选账号不匹配时，回退到 292。JWT 套餐仅用于选择目标长度，不替代上游认证。将 `gateway.openai_codex_ticket.target_length` 设置为非默认的正整数时，该值覆盖自动选择。

保留上游默认值 `enabled: false`、`fail_closed: true`。关闭功能时不打票、不注入门票；启用功能且 `fail_closed: true` 时，配置模型缺少有效门票会暂停调度。若需要缺票时继续请求，可自行设置 `gateway.openai_codex_ticket.fail_closed: false`；此选择不改变默认值。

探测响应必须为 HTTP 200、读到完整且内容一致的 `response.completed` SSE 事件，且门票前缀及目标长度校验通过后，才会缓存。读到完成事件后立即关闭探测响应，不等待连接结束；累计响应读取上限为 1 MiB。仅收到响应头、截断事件或未完成的流不会缓存门票。

门票的长度、前缀和完成事件只说明满足缓存条件，不证明模型质量，也不保证后续请求一定成功。
