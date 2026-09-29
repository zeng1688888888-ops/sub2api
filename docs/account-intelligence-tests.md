# 账号智力检测（Codex / BPS）

将已验证的 V3 实际答题检测移植到本仓库的 Excel/BPS 功能分支。

## 功能入口

- 账号列表：智力状态、实际通道、最近 24 次明确结果的色条、通过率、样本数、检测时间和复测按钮。
- 账号操作菜单：智力测试。可选择模型，查看答案、完成时间、耗时和当前会话的测试记录；结果立即同步到列表。
- 管理接口：POST /api/v1/admin/accounts/:id/intelligence-probe，可传入 model_id。

## 检测约定

默认 gpt-6-astra，使用已验证的内置糖果题和原答案校验器，推理强度 high。请求直接经过现有 OpenAI 网关，按模型映射及 BPS 选择配置确定实际通道，不重复应用模型映射。

完整正确的答案记为 healthy；完整错误的答案记为 degraded（界面显示未通过）。上游错误、超时、空输出和未完成的响应都记为 inconclusive，不参与通过率统计。单题结果不能证明模型身份，也不等于完整智力评测。

同一账号的并发检测受互斥锁约束。每次请求上限 90 秒，最多捕获 4 MiB 响应；同时兼容流式增量及只有完成事件才返回答案的响应。

## 存储和自动检测

沿用 V3 的 openai_codex_state_probe 扩展字段及 JSON 格式。该字段存放实际答题结果，与票据续接判据无关。普通账号编辑和批量编辑不能伪造统计。

最多保留同模型、同通道的 500 次明确结果。切换通道或账号身份后重新检测，旧通道统计不会当作当前结果。详情和精简账号列表均返回快照。

自动检测接入现有后台探测服务：正常约 30 分钟后再次进入候选，每批最多 20 个账号、并发 2。分布式锁及批次时限限制多实例重叠，无需新增服务或数据库迁移。

## 移植说明

检测页面沿用本仓库的弹窗、图标和国际化组件。后端直接捕获现有网关的输出，因此不依赖另一个仓库的题库、判题模型或票据采集系统。

一并补齐了原 Excel/BPS 图片测试缺失的设置仓库测试替身，并保持网关入站测试中的 API key 分组信息与真实认证结果一致。

回归同时补齐 /v1 路由组的 BPS 图片入站限制，使其与 /responses 和 /backend-api/codex 等别名使用相同检查，在读取超限请求体前返回错误。

部署时需一起更新前端和后端；沿用本仓库现有 Dockerfile 和 Compose 配置。无需使用原测试环境的 probe-test 镜像标签，也无需拷贝原环境的 .env、数据库或本地插件目录。

## Docker 部署与已有实例切换

本仓库的 Docker Compose 默认拉取 `ghcr.io/zeng1688888888-ops/sub2api:latest`。
`0.2.13` 已包含本功能；改用这个已发布镜像不需要新建 Git tag，也不需要重新编译。
仅拉取 Git 代码不会更新运行中的容器。

如果已有服务器仍使用 `weishaw/sub2api`，在**原部署目录、原 Compose 文件**中，
只把 `sub2api` 服务的 `image` 改为：

```yaml
image: ${SUB2API_IMAGE:-ghcr.io/zeng1688888888-ops/sub2api:latest}
```

在原 `.env` 中新增或修改以下一行，固定到已发布的检测版本：

```dotenv
SUB2API_IMAGE=ghcr.io/zeng1688888888-ops/sub2api:0.2.13
```

保留其他配置、密钥、端口和数据挂载，不要重新运行初始化脚本或覆盖 `.env`。
在同一部署目录执行：

```bash
docker compose config --images
docker compose pull sub2api
docker compose up -d --no-deps --force-recreate sub2api
docker compose ps sub2api
```

第一条命令显示的应用镜像应属于 `ghcr.io/zeng1688888888-ops/sub2api`。
如果原先用 `-f docker-compose.local.yml` 或其他 `-f`、`-p`、`--env-file` 参数启动，
以上命令必须沿用原参数，避免切换部署目录、项目名或数据卷。

容器启动后刷新账号管理页面，在 OpenAI OAuth / setup-token 账号的操作菜单中查看“智力测试”。
`v0.2.13` 的后台在线更新功能仍指向原作者 Release；本分支的 Docker 部署请通过上述
镜像拉取和容器重建流程更新，不要使用后台在线更新切换二进制。
