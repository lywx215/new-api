# OpenCodeGo 多账号亲和与 RPM 保护修复 Review 指南

> 实现日期：2026-08-15
> 对应审计报告：`docs/opencodego-multi-account-affinity-rpm-review-result.md`
> 范围：A1–A6、B1–B8；C组明确暂缓

## 1. 结论

A、B两批已按确认方案实现。内部Header仍为 `v1.<payload>.<signature>`，没有数据库迁移。`prompt_cache_key`来源语义不变；session、metadata和fallback使用新的 `fp2` 有界指纹，因此升级后会出现一次短期亲和缓存冷启动。

OpenCodeGo账号硬限制固定为1600。保存设置时要求有效软RPM和burst均为正数且两者之和不超过1600；旧的非法持久化值在请求路径会被收敛到安全有效值并限频告警，不会因非法配置直接关闭保护。

## 2. A组完成状态

| 编号 | 状态 | 修复后行为 | 主要代码入口 | 主要测试 |
|---|---|---|---|---|
| A1 密钥解耦 | 已修复、已测试 | 启动时缓存 `AFFINITY_SECRET`，未配置才回退 `CRYPTO_SECRET`；指纹和最终签名共用专用密钥能力，调用方拿不到原始密钥 | `common/internal_affinity.go`、`common/init.go`、`relay/common/internal_affinity.go` | `TestInternalAffinityKeyIsIndependentFromCryptoSecret`、`TestApplyInternalAffinityHeaderDoesNotChangeWhenCryptoSecretRotates` |
| A2 Redis统一时间 | 已修复、已测试 | Lua通过Redis `TIME`补充令牌；冷却只依赖键存在和PTTL，值固定为哨兵 `1`；旧冷却值因只看TTL仍兼容 | `service/opencodego_rpm.go` | `TestOpenCodeGoRPMGuardHonorsUpstreamRateLimitCooldown`、`TestOpenCodeGoCooldownValueIsOnlyASentinel` |
| A3 DB/内存一致 | 已修复、已测试 | DB路径先加载全部启用Ability，再过滤路径和排除集，最后重算优先级并按剩余权重选择 | `model/ability.go`、`model/channel_cache.go`、`service/channel_select.go` | `TestChannelSelectionFallsBackToLowerPriorityAfterHighPriorityExclusionWithAndWithoutCache` |
| A4 开关解耦 | 已修复、已测试 | `enabled`只控制旧自定义规则；`accept_internal_key`独立控制可信内部键的验签、查询和成功写回 | `service/channel_affinity.go`、管理端亲和设置页 | `TestTrustedInternalAffinityWorksWhenCustomRulesDisabled` |
| A5 严格有界指纹 | 已修复、已测试 | 先判断来源；prompt缓存键不扫描稳定内容；`fp2`直接流入HMAC，文本、工具身份和Responses输入达到预算即停，不整体Marshal schema，不拼接完整大文本 | `relay/common/internal_affinity.go` | Chat/Claude/Responses有界测试、媒体和tool output跳过测试、prompt缓存键不扫描稳定Body测试 |
| A6 Header出口隔离 | 已修复、已测试 | Task在适配器构建Header后再次显式删除内部Header；普通HTTP、Form、WebSocket和通配过滤保持原保护 | `relay/channel/api_request.go` | `TestDoTaskApiRequestRemovesInternalAffinityHeader`及既有通配透传测试 |

## 3. B组完成状态

| 编号 | 状态 | 修复后行为 | 主要代码入口 | 主要测试/验证 |
|---|---|---|---|---|
| B1 Redis日志限频 | 已修复、已测试 | Redis不可用、Lua失败、异常返回和状态Pipeline错误共用30秒告警窗口；请求继续fail-open | `service/opencodego_rpm.go` | `TestOpenCodeGoRPMWarningLimiterUsesDeterministicWindow`、fail-open测试 |
| B2 trusted_internal统计 | 已修复、已测试 | 内建规则进入 `by_rule_name`，不再计入Unknown，并可按规则清理 | `service/channel_affinity.go` | `TestTrustedInternalAffinityStatsAndRuleClear` |
| B3 RPM状态优化 | 已修复、已测试 | 保护关闭时直接返回；开启时每批最多1000渠道并使用单次Pipeline读取令牌、自然分钟计数和冷却PTTL；响应含总数和截断标记 | `service/opencodego_rpm.go`、`service/channel_affinity.go` | `TestOpenCodeGoRPMStatusesExposeGlobalTotalAndSkipWhenDisabled` |
| B4 指标范围 | 已修复、已测试 | 指标响应增加 `scope=node`、`node_name`、`reset_on_restart=true`；UI显示节点范围、来源数量/占比和fallback失败；Redis RPM状态不标为节点级 | `common/internal_affinity_metrics.go`、管理端亲和设置页 | TypeScript类型检查、生产构建 |
| B5 配置强校验 | 已修复、已测试 | relaykit定义1600常量；全局RPM、burst、cooldown和渠道覆盖均有后端校验；修改burst会扫描现有OpenCodeGo渠道；前端先保存下降值再保存上升值 | `relaykit/dto/channel_settings.go`、`controller/option.go`、`controller/channel.go`、渠道/亲和设置表单 | relaykit边界测试、Option组合测试、已有渠道冲突测试、渠道保存冲突测试 |
| B6 清理dead配置 | 已修复、已测试 | Go/TS/UI删除 `overload_policy`；历史Option读取时过滤、写入时拒绝，数据库旧值不破坏性删除 | 设置结构、`controller/option.go`、管理端类型与默认值 | `TestUpdateOptionRejectsRetiredOverloadPolicy`、类型检查 |
| B7 清理dead context key | 已修复 | 删除 `opencodego_rpm_retry_after` 写入，429等待由明确的错误返回链传递 | `middleware/distributor.go` | 根模块测试 |
| B8 429解析与入口 | 已修复、已测试 | 通用HTTP响应入口在任何协议处理前观察type 99的429；支持整数、HTTP Date、过去/非法/缺失和60秒冷却上限；429建立临时冷却但不永久禁用渠道 | `relay/channel/api_request.go`、`service/opencodego_rpm.go`、`controller/relay.go` | `TestDoRequestObservesOpenCodeGo429BeforeProtocolHandling`、`TestObserveOpenCodeGoUpstreamResponseRetryAfterFormats`、不自动禁用测试 |

## 4. 修复前后行为对比

| 场景 | 修复前 | 修复后 |
|---|---|---|
| 多节点仅统一AFFINITY_SECRET | 指纹仍受各节点CryptoSecret影响 | 指纹和签名只依赖缓存后的亲和专用密钥 |
| 应用节点时钟漂移 | 令牌补充和冷却判断可能分歧 | 令牌使用Redis时间，冷却使用Redis TTL |
| 高优先级账号全部饱和且禁用内存缓存 | DB路径可能直接无候选 | 自动落到剩余的低优先级候选 |
| 自定义规则关闭、内部亲和开启 | 内部亲和也被关闭 | 内部亲和继续验签、命中和写回 |
| 4 MiB文本或巨大tools | 可能构造/复制完整中间内容 | 稳定源最多处理配置预算，工具只取身份摘要 |
| RPM保护关闭时打开统计 | 仍逐渠道访问Redis | 不查询RPM Redis状态 |
| 非法软RPM/burst组合 | 可保存，运行时可能失去保护 | 前后端拒绝，历史非法值运行时安全收敛 |
| Task客户端伪造内部Header | 缺少Task出口最终删除 | 最终上游请求中固定不存在该Header |
| type 99真实HTTP 429 | 适配器内的观察点在真实请求路径不可达 | 通用HTTP入口在OpenAI、Claude、Responses协议分支前建立账号cooldown |
| 下层受控429经过type 60 | 错误码和 `Retry-After` 可能丢失，并触发无意义重试 | 稳定错误码 `opencodego_rpm_soft_limit` 和合法Header穿透两层；强制不重试且不禁用渠道 |
| 中间429后备用渠道成功 | Header可能过早写入响应 | `Retry-After`只保存在非JSON错误元数据，最终仍为429时才写入；最终200无该Header |

## 5. 配置与API兼容性

- `channel_affinity_setting.enabled`的新精确定义是“启用自定义亲和规则”。
- `accept_internal_key`独立生效；升级顺序仍应先开下层接受，再开上层生成。
- `overload_policy`已退休。历史数据库值被忽略且不会导致启动失败；设置查询不再返回它。
- `opencodego_rpm_limit=0`仍表示继承；正数必须小于1600，并满足有效值加全局burst不超过1600。
- 状态响应新增 `opencodego_rpm_total`、`opencodego_rpm_truncated`，内部指标新增scope字段；均为向后兼容的新增字段。
- Header名称、线格式和验签格式不变。混合版本可验签，但 `fp2` 会让非prompt-cache-key来源发生一次映射冷启动。

## 6. 实际验证结果

| 验证 | 结果 |
|---|---|
| `go test -p 1 ./...` | 通过。并行首次执行受本机Windows Application Control拦截临时测试exe；串行完整重跑全部通过 |
| `cd relaykit; GOWORK=off go test ./dto` | 通过 |
| `cd relaykit; GOWORK=off go build ./...` | 通过 |
| `bun run i18n:sync`等价执行 | Bun未安装，直接使用仓库Node脚本执行；七语言报告均为0 missing、0 extras、0 untranslated |
| `bun run typecheck`等价执行 | 直接调用同一 `tsgo -b` 入口，通过 |
| `bun run build`等价执行 | 直接调用同一Rsbuild入口，生产构建通过 |
| lint | 修改文件定向oxlint通过。全仓oxlint仍有大量与本功能无关的既有告警/错误，因此不能声明全仓lint通过 |
| 非提交型大Body性能 | 4 MiB文本、32 KiB预算：batched P99估算62.986µs；Go benchmark 20.2µs/op、约21 KiB/op、14 allocs/op。临时基准文件已删除 |
| `git diff --check` | 通过 |

## 7. 暂缓的C组事项

- C1 首次并发同键分裂：维持last-writer-wins，避免引入覆盖上游调用周期的分布式锁。
- C2 自然分钟统计：仅用于展示，保护正确性仍由Token Bucket决定。
- C4 下层旧Claude/Codex模板合并：标准拓扑由上层处理，本轮不改变。
- C5 亲和指标Redis聚合/长期持久化：本轮明确节点范围，不增加每请求Redis写入。

## 8. 多节点部署与灰度检查表

- [ ] 上下层所有节点使用完全一致的 `AFFINITY_SECRET`；未显式配置时才允许共同回退到一致的 `CRYPTO_SECRET`。
- [ ] 所有下层实例连接同一个Redis。
- [ ] 先部署下层并开启 `accept_internal_key`，确认旧客户端无变化。
- [ ] 开启RPM保护前确认默认RPM + burst不超过1600，并处理界面报告的渠道覆盖冲突。
- [ ] 先小流量开启上层 `generate_internal_key`，观察签名失败、亲和命中、迁移和真实429。
- [ ] 多节点看亲和指标时按 `node_name`分别判断；进程重启后计数会清零。
- [ ] RPM状态若出现截断，使用返回的total确认实际规模；剩余令牌“不可用”通常表示Redis状态不存在或读取失败。
- [ ] 升级窗口接受session/metadata/fallback的短期缓存冷启动；prompt_cache_key来源不受fp2变化影响。

## 9. 给Review模型的逐项问题

1. 是否存在任何请求路径仍直接读取 `CryptoSecret` 生成稳定指纹或内部Header？
2. Token Bucket Lua是否完全不接收应用时间，并只用Redis `TIME`和冷却PTTL？
3. DB和内存路径是否都在排除饱和渠道后重算优先级与权重？
4. `enabled=false, accept_internal_key=true` 时，可信内部缓存能否查询且成功后写回，同时旧规则确实不匹配？
5. Chat、Claude、Responses提取是否可能在预算前整体Marshal、拼接或复制大型schema/媒体/tool output？
6. 普通HTTP、Form、WebSocket、Task和通配Header出口是否都无法泄露内部Header？
7. Redis所有fail-open分支是否受同一30秒窗口限制，且没有把Redis故障变成请求失败？
8. `trusted_internal`统计和按规则清理是否与缓存键格式一致？
9. RPM状态是否在关闭保护时零Redis查询，开启时使用Pipeline且最多返回1000条？
10. 所有保存入口是否共同保证有效软RPM + burst不超过1600，修改burst时是否检查现存渠道？
11. 429整数/日期解析是否处理过去时间、非法值、缺失值和60秒封顶，并在协议分支前冷却？
12. `overload_policy`和dead context key是否只保留兼容过滤/审计说明，没有任何运行时依赖？
13. 实际type 99 HTTP 429是否在所有协议适配器之前建立cooldown，且缺失/非法Header使用配置默认值？
14. `opencodego_rpm_soft_limit`经过type 60后是否保持错误码与合法 `Retry-After`，同时不重试、不禁用type 60或type 99？
15. 发生中间429但备用渠道成功时，最终200是否确认不携带 `Retry-After`？

## 10. 测试报告后续纠偏（2026-08-15）

旧测试Run暴露出两项上线阻断缺陷：OpenCodeGo适配器的429分支并不位于真实通用HTTP请求路径，且type 60转发会丢失下层受控429的Header/稳定语义。现已把观察点前移至 `doRequest` 的真实响应入口；`NewAPIError`使用未导出的非JSON字段携带合法 `Retry-After`；最终Controller仅在最终状态仍为429时输出Header。

测试编排同步改为attempt隔离：低RPM场景使用唯一缓存键并精确清理三个目标渠道的RPM/cooldown键，缺失Header直接失败，所有结果带attempt并在失败时保留部分产物。Mock测试拆成协议自检和经过live type 60/type 99链路的网关E2E。旧Run目录保持不变，修复后的运行必须使用新Run ID。

本次纠偏后的确定性验证结果：根模块 `go test -p 1 ./...` 通过；relaykit在 `GOWORK=off` 下测试和独立构建通过；七语言同步报告均为0 missing、0 extras、0 untranslated；变更前端文件定向lint、typecheck和生产构建通过；`git diff --check`通过。全仓lint仍有既有错误，继续作为非阻断基线单独保存。Windows原有HTTP/2 GOAWAY测试的立即关连接存在WSAECONNRESET竞态，本轮改为保持draining连接直到客户端完成重试，并连续10次验证通过；“无GetBody”测试改为断言实际不重试，不再绑定Go内部错误文案。

运行环境已使用测试专用随机 `AFFINITY_SECRET`、`CRYPTO_SECRET`、`SESSION_SECRET` 和临时root PAT完成原地升级，密钥只保存在Git忽略的Run目录，不写入报告。权威检查点为 `ocg-e2e-20260815-10`：代码验证、Redis、Mock协议、完整type 60/type 99 Mock网关、亲和来源及双实例拓扑均已通过；上层/下层SQLite完整性均为 `ok`。

实测还发现并修复了三项测试可靠性问题：新建渠道尚未进入内存缓存时增加有界就绪轮询并重置Mock状态；复用Mock渠道时不再把禁止出现在PATCH中的 `status` 字段发送给更新接口；日志观察改用响应请求ID关联type 60与type 99链路，双实例的分库链路则使用下层日志边界。该关联测试进一步暴露 `final_channel_id` 在重试成功时仍是旧渠道，现已在重试渠道选定后、消费日志写入前更新遥测，亲和映射仍只在最终成功后迁移。

最终权威测试Run为 `ocg-e2e-20260816-13`，结论为 `PARTIAL`。三个真实账号密钥已确认互不相同，三账号低RPM迁移、缓存冷到热重建、受控429与 `Retry-After`、Redis真实断连fail-open、完整type 60/type 99 Mock网关和双实例拓扑均已通过。累计真实请求107次，估算费用0.096953美元。用户明确决定跳过真实1550/1650/1750 RPM、真实上游429后的缓存迁移和三客户4800 RPM，因此不得声称已验证OpenCodeGo账号真实1600 RPM硬限制或高并发稳定性。当前双实例和测试数据继续保留且未清理，详细状态见 `docs/opencodego-affinity-rpm-test-checkpoint.md`。
