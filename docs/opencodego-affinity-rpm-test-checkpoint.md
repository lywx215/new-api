# OpenCodeGo 亲和/RPM测试当前检查点

更新时间：2026-08-16 17:10（Asia/Shanghai）

## 2026-08-16 opencodego.5 自动禁用补丁更新

- 上、下层双实例均已升级到 `v1.0.0-rc.24-opencodego.5`，继续使用现有独立SQLite、共享Redis、Type 60回环渠道、测试Token和三个真实Type 99渠道；未执行环境恢复或全局清理。
- 升级检查点位于Run 10目录的 `checkpoint-opencodego5-20260816-165826`；升级前后上下层SQLite `PRAGMA integrity_check` 均为 `ok`。
- 下层已开启 `AutomaticDisableChannelEnabled`，并在保留原关键词的基础上增加 `enable usage from your available balance`；三个真实Type 99渠道均保持 `auto_ban=true`。
- 低负载真实业务链路发送1个请求：渠道1返回周额度429，下层记录 `auto-disable: true, auto-disable reason: keyword`，渠道状态由启用变为自动禁用。随后重试路径返回受控软限流429及 `Retry-After`，不会把该本地错误误判为永久禁用依据。
- 验证完成后已通过管理API重新启用渠道1，并精确清理渠道1的RPM/cooldown测试键；当前渠道1、2、3均为启用状态且 `auto_ban=true`。
- 真实上游正文命中关键词后，重试路径把最终客户端响应替换为本地软限流错误，因此验收产物分别记录 `client_response_keyword_matched=false` 和 `disable_decision_keyword_matched=true`；禁用依据以服务端策略日志为准。
- 本次只增加1个真实请求，不属于高压测试；累计预算更新为108个真实请求、估算0.096953美元。用户先前决定跳过的真实1600 RPM和三客户4800 RPM场景仍保持跳过，最终总体评级仍为 `PARTIAL`。
- 本次低负载验收使用新Run ID `ocg-e2e-20260816-14`，从Run 13继承预算；脱敏产物为该目录的 `opencodego5-live-keyword-disable.json`。原始密钥和完整Token不得写入文档或提交。
- 收口Review补充了 `status_code_mapping` 边界修复和错误日志脱敏。最终源码及全量测试通过，但本机Smart App Control拒绝启动新生成的未签名Windows二进制；安全重启脚本自动恢复了检查点 `checkpoint-opencodego5-final-20260816-172137`。当前上下层继续健康运行已完成真实关键词验收的 `.5` 构建（SHA-256前8位 `7b8af79b`），数据库、配置和三个渠道状态均已复核，SQLite完整性为 `ok`。映射边界增量只完成代码级验收，不能声称已在当前双实例运行验证。

## 2026-08-16 Run 13 更新

- 当前权威Run：`ocg-e2e-20260816-13`，累计真实请求107次，估算费用0.096953美元。
- 三个真实渠道完整密钥互不相同，三个hard-limit Token低成本预检均返回200。
- 双实例低RPM迁移通过，渠道顺序为2→3→1：渠道3首次迁移仍为热缓存；渠道1首次为冷缓存，第二次恢复到7168/7290缓存Token；全部饱和返回带`Retry-After: 45`的受控429，等待后恢复成功。
- Mock网关E2E通过：三个隔离账号均完成冷→热、实际429冷却、等待恢复、内部Header不泄漏和Redis Lua错误fail-open。
- 专用Redis真实断连期间3个真实请求均返回200，30秒窗口只记录1条fail-open告警；Redis已重启并重新通过TIME、Lua、PTTL和Pipeline探针。
- 双实例节点指标、签名链路和SQLite完整性再次通过；上层已恢复`generate=true/accept=false/rpm=false`，下层已恢复`generate=false/accept=true/rpm=true/1450+50`。
- 用户于2026-08-16明确决定不再执行真实高压。`live-gate`、真实1550/1650/1750 RPM硬限制、真实429后缓存迁移和三客户4800 RPM均标记为`skipped`，最终结论为`PARTIAL`，不能声称已验证账号真实1600 RPM硬限制。
- 本次决定后未发送任何高压请求；累计预算仍为107次真实请求、估算0.096953美元。
- 当前双实例、SQLite、三个真实渠道、Type 60回环渠道和专用Redis均继续保留，未执行清理或回滚，以便后续进行非高压验证。

Run 12保留一次测试工具失败审计：业务请求实际在下层渠道3成功，但工具错误地用外层请求ID直接查询下层SQLite。工具现已支持通过上层`upstream_request_id`跨数据库精确关联，并有双SQLite确定性回归测试；Run 13迁移证明修复有效。

## 2026-08-16 Run 11 更新

- 当前权威Run：`ocg-e2e-20260816-11`，已从Run 10继承预算。
- 渠道1的新密钥最初只保存到了上层3000；现已通过管理API同步到实际发送OpenCodeGo请求的下层3001。
- 同步后渠道1低成本预检返回200，原周额度429已消失。
- 自动清点确认渠道1 `go-a` 与渠道3 `go-c` 的密钥指纹完全相同，因此它们是同一OpenCodeGo账号，不满足三个独立账号的验收前提。
- 当前累计真实请求82次，估算费用0.076463美元；真实1600 RPM和4800 RPM测试均未执行。
- `verify-channels`已标记为`needs_manual`，本轮没有执行清理、回滚或高压请求。
- 测试工具已增加重复真实账号密钥检查；Mock渠道被明确排除，避免因其固定测试密钥产生误报。

当时继续前必须把渠道1或渠道3替换为第三个独立账号，并同时更新上层3000和下层3001。该条件后来已经完成；不得据此重新启动已被用户取消的高压测试。

## 历史Run 10结论（已被Run 13和本补丁验收取代）

- Run 10为上一轮审计记录；其结论已由上面的Run 11更新取代。
- 当时总体状态：`BLOCKED`，阻塞来自真实账号额度，不是代码或SQLite损坏。
- 累计真实请求：77 / 10000。
- 累计估算费用：0.076434 / 10美元。
- 未执行清理或回滚；测试渠道、Token、Redis和数据库均保留。

已通过：

- 根模块 `go test -p 1 ./...`。
- relaykit在 `GOWORK=off` 下测试和独立构建。
- i18n七语言0 missing、0 extras、0 untranslated。
- 修改前端文件定向lint、TypeScript检查和生产构建。
- Redis `PING`、`TIME`、Lua、PTTL、Pipeline和Token Bucket探针。
- Mock协议第1601请求429、整数/HTTP Date `Retry-After`。
- 完整type 60 → type 99 Mock网关：三账号逐个冷→热、账号迁移、全部饱和受控429、等待恢复、Header不泄漏、Redis fail-open。
- 真实亲和来源和签名链路。
- 双实例SQLite拓扑、节点级指标和内部Header链路。
- 上层、下层SQLite `PRAGMA integrity_check=ok`。

## 历史Run 10阻塞

真实渠道1 `go-a` 返回：

```text
Weekly usage limit reached. Resets in about 22 hours.
```

因此低RPM实测只能观察到渠道3 → 渠道2，随后返回受控429；不能声称完成三账号迁移，也不能进入真实1600 RPM和三客户4800 RPM验收。

渠道2、3的实测仍证明：

- 亲和账号达到软限制后会迁移。
- 全部可用账号饱和时返回 `opencodego_rpm_soft_limit`。
- type 60向客户端保留 `Retry-After: 50`。
- 等待Header指定时间加安全余量后请求恢复成功。
- 本轮相同长前缀在迁移前后均返回7168缓存Token；应记录为“真实上游缓存可能跨账号共享”，不能伪称新账号首次必然冷。

## 当前运行拓扑

- 上层：`http://127.0.0.1:3000`，SQLite为Run目录内 `upper.db`，`NODE_NAME=ocg-upper-test`。
- 下层：`http://127.0.0.1:3001`，SQLite为Run目录内 `lower.db`，`NODE_NAME=ocg-lower-test`。
- 两实例共享测试Redis和同一 `AFFINITY_SECRET`。
- 上层：生成内部亲和键，关闭RPM保护。
- 下层：接受内部亲和键，启用RPM保护，生产建议值1450 RPM、burst 50、cooldown 10秒。
- 原 `one-api.db` 未删除，当前不被双实例使用，保留低RPM阶段检查点。
- 敏感值只在 `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/runtime-secrets.local.json` 和 `secrets.local.json`；禁止输出或提交。

当前双实例PID和二进制路径以Run目录的 `dual-instance-processes.json` 为准，不要按进程名批量停止。

## 历史Run 10继续条件（现已完成，不得作为当前操作指令）

1. 在下层管理界面（3001）替换渠道1为具有独立可用额度的OpenCodeGo账号，或等待原账号周额度重置。
2. 对渠道1执行一次 `deepseek-v4-flash` 渠道测试，确认不再返回套餐/余额/周额度429。
3. 确认三个账号均为专用测试账号且没有其他流量。
4. 如后续还需恢复单实例测试，把同一账号变更同步到保留的 `one-api.db`；不要在报告或脚本中保存原始密钥。
5. 通知测试模型继续；不要运行cleanup或rollback。

## 历史高压计划（用户已取消，禁止执行）

以下顺序仅为旧审计记录。用户已明确决定不执行真实1600 RPM、真实429后高压缓存迁移和三客户4800 RPM；其他模型不得根据本节启动这些测试。当前只允许继续低负载、确定性Mock、配置检查、健康检查和代码回归，除非用户以后重新明确授权高压测试。

代码和测试工具在Run 10之后有可靠性修订，不能继续复用其输入Hash。恢复后创建新Run ID，并从Run 10继承77次请求和0.076434美元预算，然后：

1. 保存新的上下层配置、渠道和Redis快照。
2. 三个hard-limit Token各发送一次低成本预检；任何套餐/余额类429立即回到 `needs_manual`。
3. 精确清理三个真实渠道的RPM/cooldown键和本attempt亲和映射。
4. 重跑三账号低RPM缓存迁移。
5. 执行迁移失败和Redis故障专项。
6. 进入真实1600 RPM人工确认门。
7. 验证真实429后的缓存迁移。
8. 执行三客户4800 RPM。
9. 执行中断恢复、最终报告；最后才单独确认清理。

## 关键产物

- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/summary.md`
- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/cache-migration-attempt-1-summary.json`
- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/mock-gateway-e2e.json`
- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/dual-instance-smoke.json`
- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/configuration-changes.md`
- `.local-tests/opencodego-affinity-rpm/ocg-e2e-20260815-10/requests.ndjson`

旧Run和diagnostic目录作为审计记录保留，不续跑、不覆盖。
