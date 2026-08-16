# OpenCodeGo `v1.0.0-rc.24-opencodego.4` 发布说明

发布日期：2026-08-16
适用对象：使用多个OpenCodeGo账号并通过New API上下层转发的部署管理员

## 主要变化

- 新增签名内部亲和键，上层type 60生成、下层验签和路由，客户无需修改请求。
- 支持 `prompt_cache_key`、`x-opencode-session`、可选 `metadata.user_id` 和有界稳定指纹来源。
- 增加OpenCodeGo账号级Redis Token Bucket，支持软RPM、burst、临时冷却和饱和账号迁移。
- 所有账号饱和时返回稳定错误码 `opencodego_rpm_soft_limit` 和有效 `Retry-After`。
- type 99真实HTTP 429会在协议处理前建立临时冷却，不会永久禁用渠道。
- type 60可透传下层的受控429错误码和 `Retry-After`；中间429后备用渠道成功时最终200不会携带该Header。
- 自动分组会在当前组全部饱和后继续尝试后续组；DB和内存渠道选择路径保持一致。
- 内部Header在普通HTTP、Form、WebSocket、Task和通配透传出口均被移除。
- 管理端增加亲和来源、节点级指标、账号RPM状态、迁移和冷却观测，并完成七种语言翻译。

## 兼容性与升级注意事项

- 不新增数据库字段或迁移。
- OpenCodeGo渠道类型仍为 `99`，New API渠道类型仍为 `60`。
- 内部Header名称和 `v1.<payload>.<signature>` 线格式保持不变。
- 建议所有上下层节点显式配置相同的 `AFFINITY_SECRET`；未配置时才回退到一致的 `CRYPTO_SECRET`。
- session、metadata和fallback来源使用 `fp2` 有界指纹，升级时会出现一次亲和缓存冷启动；`prompt_cache_key`来源语义不变。
- `channel_affinity_setting.enabled` 仅控制旧自定义亲和规则；可信内部亲和由 `accept_internal_key` 独立控制。
- `overload_policy` 已退休，历史数据库值会被忽略。
- `opencodego_rpm_limit=0` 继续表示继承全局值；有效软RPM与burst之和不得超过账号硬限制1600。

推荐生产值：

```text
default_account_rpm=1450
account_burst=50
rate_limit_cooldown_seconds=10
```

推荐部署顺序：先升级下层并配置共享Redis和密钥，开启 `accept_internal_key` 与RPM保护；再升级上层，小流量开启 `generate_internal_key` 后逐步全量。

## 验证结果与边界

已通过根模块全量Go测试、relaykit独立测试和构建、七语言i18n同步、前端类型检查、变更文件定向lint和生产构建。全仓lint仍存在与本功能无关的既有基线错误。

真实测试已验证三账号低RPM迁移、缓存冷到热重建、受控429、Redis断连fail-open、Mock实际上游429冷却和双实例type 60/type 99链路。用户决定不执行真实高压，因此本版本没有验证真实账号1550/1650/1750 RPM边界、真实上游429后的缓存迁移或三客户4800 RPM；测试总评为 `PARTIAL`，不应解读为真实1600 RPM硬限制已完成验收。

完整部署和测试说明：

- `docs/opencodego-affinity-rpm-production-deployment.md`
- `docs/opencodego-affinity-rpm-test-runbook.md`
- `docs/opencodego-affinity-rpm-test-checkpoint.md`
- `docs/opencodego-multi-account-affinity-rpm-fix-review.md`
