# OpenCodeGo 多账号亲和 & RPM 保护 — 代码 Review 报告

> **Review 日期**: 2026-08-15
> **Review 范围**: 15+ 源文件, 10 测试文件
> **基准文档**: [`opencodego-multi-account-affinity-rpm-review.md`](./opencodego-multi-account-affinity-rpm-review.md)

---

## 总体评价

**方案设计合理，实现质量较高，安全防御多层。** 核心路径（HMAC签名验证、Lua原子令牌桶、排除集选择、成功写回保护）的正确性已通过代码分析和测试用例确认。建议在灰度部署前优先解决 P1 问题。

| 严重级别 | 数量 | 说明 |
|---------|------|------|
| **P0** — 阻塞上线 | 0 | 无 |
| **P1** — 建议上线前修复 | 2 | CRYPTO_SECRET耦合、Token Bucket时钟源 |
| **P2** — 建议近期修复 | 6 | Dead context key、DB/内存路径差异等 |
| **P3** — 低优先级改进 | 4 | 监控精度、env缓存等 |

---

## 按文档 Q1–Q12 逐项回答

### Q1. 亲和命中、RPM预占、备用选择、成功写回之间是否存在双重扣令牌或漏扣？

**结论: 无双重扣除或漏扣。** (P3 — 仅有极端边缘情况)

**分析:**

- Lua 脚本 ([`opencodego_rpm.go:18–41`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L18-L41)) 在 Redis `EVAL` 中原子执行，单次调用要么扣 1 个令牌要么不扣，不可能双重扣除。
- 亲和命中路径 ([`distributor.go:132`](file:///G:/code/gemini30/new-api/middleware/distributor.go#L132)): `TryReserveOpenCodeGoRPM(c, channel)` 对亲和渠道预占 1 个令牌。
- 亲和拒绝后，`channel = nil` ([`distributor.go:135`](file:///G:/code/gemini30/new-api/middleware/distributor.go#L135))，进入 `CacheGetRandomSatisfiedChannel` → `selectChannelWithRPMCapacity` → 对备选渠道再次调用 `TryReserveOpenCodeGoRPM`。两次调用的 `channel.Id` 不同，无重复扣除。
- **唯一的"浪费"场景**: 亲和渠道预占了令牌但被拒绝（remaining < 1），此时已消耗的令牌不会退还。但这不是双重扣除 — 令牌桶在拒绝前已经判定 `tokens < 1`，扣除发生在 `tokens >= 1` 分支（[L35](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L35)），拒绝分支不扣除（[L30–33](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L30-L33)）。
- `KEYS[3]` (rpm_count) 是独立的监控计数器，与令牌桶逻辑无交互。
- 成功写回 ([`channel_affinity.go:798–833`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L798-L833)) 仅写缓存映射，不涉及令牌操作。

---

### Q2. 首次并发请求使用同一亲和键时是否会产生缓存分裂？

**结论: 会产生短暂的缓存分裂，但属于可接受的 eventual consistency。** (P3)

**分析:**

- 亲和缓存查找 ([`channel_affinity.go:667`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L667)) 和成功写回 ([`channel_affinity.go:830`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L830)) 不是原子操作。
- 场景：两个并发请求使用相同亲和键，缓存中无映射 → 两个请求都缓存 miss → 各自随机选择不同渠道 → 各自成功后写回不同 `channelID` → **最后一个写入者胜出**。
- 影响：下一个请求将使用最后写入的渠道，另一个渠道的缓存优势丢失。
- **这不需要短锁或一致性哈希**，因为：
  1. 这是一次性收敛过程 — 后续请求将命中缓存使用同一渠道
  2. 在高并发冷启动场景中，即使用了锁，锁持有时间需要覆盖整个上游请求（不可行）
  3. 一致性哈希会引入额外的映射层，与现有权重/优先级选择逻辑冲突
- **改进建议**: 可考虑 Redis `SETNX` 语义，但收益有限。

---

### Q3. 自动分组跨组回退是否可能越过用户权限、错误计费组或破坏原重试语义？

**结论: 不会越过权限。计费和重试语义正确。** (无问题)

**分析:**

- [`channel_select.go:116`](file:///G:/code/gemini30/new-api/service/channel_select.go#L116): `GetRequestAutoGroups(ctx, userGroup)` 调用 `FilterUserTokenAutoGroups`，其中通过 `IsUserSelectableGroup(userGroup, group)` 校验每个候选组的权限。只有用户被授权访问的组才会进入候选列表。
- 跨组回退循环 ([`channel_select.go:134–197`](file:///G:/code/gemini30/new-api/service/channel_select.go#L134-L197)) 只遍历预过滤后的 `autoGroups`，不会引入未授权组。
- 切换组时重置 `param.SetRetry(0)` ([L157](file:///G:/code/gemini30/new-api/service/channel_select.go#L157), [L171](file:///G:/code/gemini30/new-api/service/channel_select.go#L171))，新组从最高优先级开始，不会跳过优先级。
- 计费组通过 `ContextKeyAutoGroup` 正确传递到下游 ([L174](file:///G:/code/gemini30/new-api/service/channel_select.go#L174))。
- 非 `OpenCodeGoRPMError` 的错误立即返回 ([L160](file:///G:/code/gemini30/new-api/service/channel_select.go#L160))，不会错误跨组。

---

### Q4. 内存缓存路径和数据库选择路径是否都正确排除饱和渠道，并保持优先级/权重语义？

**结论: 内存缓存路径正确。数据库路径存在轻微行为差异。** (P2)

**分析:**

- **内存缓存路径** ([`channel_cache.go:118–215`](file:///G:/code/gemini30/new-api/model/channel_cache.go#L118-L215)):
  - `filterExcludedChannels` 在优先级分桶**之前**执行 ([L129](file:///G:/code/gemini30/new-api/model/channel_cache.go#L129))
  - 排除后重新计算优先级层 → 排除高优先级渠道后自动落到低优先级层 ✅
  - 权重随机选择在剩余候选中正确执行 ✅

- **数据库路径** ([`ability.go:148–196`](file:///G:/code/gemini30/new-api/model/ability.go#L148-L196)):
  - 优先级在查询中确定（基于所有渠道，包括将被排除的）
  - 排除在查询结果上**之后**应用 ([L165–173](file:///G:/code/gemini30/new-api/model/ability.go#L165-L173))
  - 如果某个优先级层的所有渠道都被排除，函数返回 `nil` — 不会自动落到下一优先级层

- **实际影响有限**: 当 Redis 不可用时（fail-open），RPM guard 不生效，排除集为空。此差异主要影响未来其他排除场景。

> [!IMPORTANT]
> **建议**: 在 DB 路径中对排除后的空结果增加优先级降级重试，或文档明确标注此差异。

---

### Q5. 真实429冷却是否在所有响应协议和流式路径中执行？

**结论: 是的，所有路径都覆盖。** (无问题)

**分析:**

[`opencodego/adaptor.go:201–212`](file:///G:/code/gemini30/new-api/relay/channel/opencodego/adaptor.go#L201-L212) 中的 429 检测和冷却设置位于协议分支**之前**：

```go
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
    if resp != nil && resp.StatusCode == http.StatusTooManyRequests && info != nil {  // L202
        // 冷却设置 — 在所有协议处理之前执行
        service.MarkOpenCodeGoRateLimited(info.ChannelId, retryAfter)  // L211
    }
    // 然后才进入 protocol switch...
```

覆盖：
- ✅ OpenAI 协议（流式/非流式）
- ✅ Claude/Anthropic 协议（流式/非流式）
- ✅ Responses 协议（流式/非流式，包括跨协议转换场景）

---

### Q6. 内部Header是否可能通过任何路径泄露到最终上游？

**结论: 三层防御，无泄露路径。** (无问题)

**分析:**

| 防御层 | 位置 | 机制 |
|--------|------|------|
| 1. 生成端控制 | [`newapi/adaptor.go:101`](file:///G:/code/gemini30/new-api/relay/channel/newapi/adaptor.go#L101) | 仅 New API type 60 渠道调用 `ApplyInternalAffinityHeader` |
| 2. 透传跳过列表 | [`api_request.go:96`](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L96) | `passthroughSkipHeaderNamesLower` 包含 `x-newapi-affinity-key` |
| 3. 显式删除 | [`api_request.go:338–339`](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L338-L339), [L373–374](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L373-L374), [L402–403](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L402-L403) | `DoApiRequest`, `DoFormRequest`, `DoWssRequest` 三个函数都对非 NewAPI 类型渠道执行 `Header.Del` |

- WebSocket 路径 (`DoWssRequest`) 也有删除 ✅
- `DoTaskApiRequest` 没有删除逻辑，但 Task 适配器不调用 `ApplyInternalAffinityHeader`，且客户端传入的同名 header 会被上层覆盖 ✅

> [!NOTE]
> `DoTaskApiRequest`（[`api_request.go:572–600`](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L572-L600)）缺少显式的 `Header.Del` 调用。虽然当前不会产生泄露（Task 适配器不生成此 header），但作为防御性编程建议补上。(P3)

---

### Q7. `CRYPTO_SECRET` 和 `AFFINITY_SECRET` 的职责是否应完全解耦？

**结论: 应当解耦。当前耦合是最重要的部署风险。** (P1)

**分析:**

当前实现中存在双密钥耦合：

```
stableRequestFingerprint()  →  HMAC(CryptoSecret, content)     // relay/common/internal_affinity.go:99
SignInternalAffinitySource() →  HMAC(affinitySecret(), source)  // common/internal_affinity.go:26-31
```

- `affinitySecret()` ([`common/internal_affinity.go:13–18`](file:///G:/code/gemini30/new-api/common/internal_affinity.go#L13-L18)) 优先使用 `AFFINITY_SECRET` env，回退到 `CryptoSecret`。
- `stableRequestFingerprint` 始终使用 `CryptoSecret`（[`relay/common/internal_affinity.go:99`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L99)）。

**问题**: 多上层节点只统一了 `AFFINITY_SECRET` 但 `CRYPTO_SECRET` 不一致时：
1. 签名验证通过 ✅（使用统一的 AFFINITY_SECRET）
2. 但 payload 不同（fingerprint 用不同 CRYPTO_SECRET 产生不同 HMAC）
3. 结果：同一用户的相同请求在不同上层节点生成不同亲和键，缓存完全无法命中

> [!WARNING]
> **建议**: 让 `stableRequestFingerprint` 也使用 `affinitySecret()` 或普通 SHA-256（不依赖密钥），或在灰度文档中强调**必须同时统一两个密钥**。

---

### Q8. 稳定指纹是否可能包含敏感原文、工具输出、thinking或大块媒体数据？

**结论: 不会包含原文。指纹是 HMAC 摘要。** (无问题)

**分析:**

1. `stableRequestFingerprint` 收集内容后执行 `GenerateHMACWithKey` ([L99](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L99))，产生不可逆摘要
2. `SignInternalAffinitySource` 对摘要再次 HMAC → Base64 ([`common/internal_affinity.go:26–31`](file:///G:/code/gemini30/new-api/common/internal_affinity.go#L26-L31))
3. 最终 header 中无任何原文

内容过滤：
- `data:` URL 被跳过 ([L24–26](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L24-L26)) ✅
- `StringContent()` 仅提取 `type=text` 内容部分 ✅
- Responses 的 `function_call_output` 被跳过 ([L127–129](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L127-L129)) ✅
- `max_source_bytes` 截断大内容 ([L21](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L21)) ✅
- 日志中使用短指纹 `affinityFingerprint` (SHA1 前 8 字符) ✅

**唯一的中间过程**: `affinitySourceCollector.builder` 在进程内存中短暂持有明文（system prompt、tools、first user message），但不持久化、不传输、不记录日志。GC 后清除。

---

### Q9. Redis故障、超时、Lua返回类型异常时是否始终安全 fail-open 且不会日志风暴？

**结论: fail-open 正确。日志风暴风险存在但可控。** (P2)

**分析:**

[`TryReserveOpenCodeGoRPM`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L76-L110) 有 3 个 fail-open 点：

| 行号 | 条件 | 处理 |
|------|------|------|
| L86–89 | Redis 不可用 | `SysError` + return `(true, 0, -1)` |
| L97–99 | `Eval` 返回 error | `SysError` + return `(true, 0, -1)` |
| L101–104 | 返回值格式异常 | `SysError` + return `(true, 0, -1)` |

**fail-open 正确** — 所有路径返回 `allowed=true`。✅

**日志风暴风险**:
- L86–89 (`Redis is unavailable`): 每次请求都打印。如果 Redis 宕机且 QPS 高，会产生大量日志。
- L97–99: 同上，每次 Eval 失败都打印。
- 亲和签名验证失败的日志已有频率限制（每 60 秒一次，[`channel_affinity.go:606–608`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L606-L608)），但 RPM 相关的 `SysError` 没有。

> [!IMPORTANT]
> **建议**: 对 `TryReserveOpenCodeGoRPM` 中的 Redis 故障日志增加类似 `lastInvalidInternalAffinityWarning` 的频率限制（如每 30 秒一次）。

---

### Q10. 所有账号饱和返回的 `Retry-After` 是否在首次分配和重试路径都正确传给客户端？

**结论: 正确传递。** (无问题)

**分析:**

| 路径 | 代码位置 | 行为 |
|------|----------|------|
| 首次分配 | [`distributor.go:157–160`](file:///G:/code/gemini30/new-api/middleware/distributor.go#L157-L160) | `AsOpenCodeGoRPMError(err)` → `c.Header("Retry-After", ...)` → HTTP 429 |
| 重试路径 | [`controller/relay.go:322–324`](file:///G:/code/gemini30/new-api/controller/relay.go#L322-L324) | `AsOpenCodeGoRPMError(err)` → `c.Header("Retry-After", ...)` → 429 + `SkipRetry` |
| 自动组汇总 | [`channel_select.go:198–202`](file:///G:/code/gemini30/new-api/service/channel_select.go#L198-L202) | 所有组饱和 → `OpenCodeGoRPMError{RetryAfter: minimumRPMWait}` |

`minimumRPMWait` 来自 Lua 脚本的计算 `max(1, ceil((1-tokens)*60/rpm))` — 基于实际令牌恢复速率。

---

### Q11. 旧 Claude/Codex模板在真实上下层部署中是否需要由下层再次执行？

**结论: 当前拓扑不需要，但边界场景需要文档化。** (P2)

**分析:**

- 可信内部亲和验证成功后 **提前返回** ([`channel_affinity.go:596–602`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L596-L602))，跳过旧规则匹配循环 ([L618–677](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L618-L677))。
- 这意味着下层不会执行旧 Claude/Codex 规则的 `ParamOverrideTemplate`。
- **当前拓扑**: 上层已经通过旧规则设置了 Header Override（如 `Originator`, `Session_id`），这些 Override 作为渠道配置透传到下层。
- 测试 [`TestApplyInternalAffinityHeaderPreservesExistingChannelHeaderOverrides`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go#L56) 确认添加内部 Header 不会覆盖已有的 Override。
- 测试 [`TestInvalidInternalAffinityHeaderFallsBackToExistingRule`](file:///G:/code/gemini30/new-api/service/channel_affinity_template_test.go#L267) 确认签名无效时回退到旧规则。

> [!NOTE]
> 如果业务要求"下层也必须根据旧 Claude/Codex 规则补充 Header"，当前的提前返回会阻止此行为。需要在 `GetPreferredChannelByAffinity` 中合并可信内部规则和旧模板。

---

### Q12. 管理统计是否准确反映多节点、滚动一分钟和可信内部缓存？

**结论: 存在已知精度限制。** (P2)

**分析:**

| 统计项 | 精度 | 问题 |
|--------|------|------|
| 内部亲和指标 | 单进程 | `atomic.Int64` 不跨节点聚合，重启清零 ([`internal_affinity_metrics.go`](file:///G:/code/gemini30/new-api/common/internal_affinity_metrics.go)) |
| 当前分钟请求量 | 自然分钟 | `rpm_count:{channelID}:{unix_minute}` 是自然分钟桶，非滑动窗口 ([`opencodego_rpm.go:93`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L93)) |
| RPM 状态查询 | N+1 | `GetOpenCodeGoRPMStatuses` 逐渠道 3 次 Redis 调用 ([`opencodego_rpm.go:130–146`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L130-L146)) |
| `trusted_internal` 缓存统计 | 归为 Unknown | 缓存键前缀解析不包含 `trusted_internal` 作为内建规则 ([`channel_affinity.go:155–190`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L155-L190)) |

> [!IMPORTANT]
> **建议**:
> 1. RPM 状态查询改用 Pipeline 减少 Redis 调用
> 2. `GetChannelAffinityCacheStats` 的 `byRuleName` 解析增加对 `trusted_internal` 前缀的识别
> 3. 文档明确指标是进程级、非持久化

---

## 额外发现（超出 Q1–Q12）

### P1-2: Token Bucket 使用应用节点时间 (P1)

**文件**: [`opencodego_rpm.go:96`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L96)

Lua 脚本通过 `ARGV[1] = time.Now().UnixMilli()` 接收应用节点时间：

```go
result, err := common.RDB.Eval(ctx, openCodeGoTokenBucketScript,
    keys, time.Now().UnixMilli(), rpm, burst).Result()
```

Lua 中的 refill 计算 ([L29](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L29)):
```lua
tokens = math.min(capacity, tokens + math.max(0, now - ts) * rpm / 60000)
```

如果两个应用节点的时钟有偏差（如 Node A 比 Node B 快 2 秒），Node A 写入的 `ts` 会大于 Node B 的 `now`，导致 `now - ts < 0` → `max(0, ...)` → 不补充令牌。反之则过度补充。

> [!WARNING]
> **建议**: 改用 Redis `TIME` 命令获取服务端时间。可在 Lua 脚本开头 `local now = redis.call('TIME'); now = tonumber(now[1]) * 1000 + tonumber(now[2]) / 1000`。这消除了跨节点时钟偏差。

### P2-1: Dead Context Key `opencodego_rpm_retry_after` (P2)

**文件**: [`distributor.go:139`](file:///G:/code/gemini30/new-api/middleware/distributor.go#L139)

```go
c.Set("opencodego_rpm_retry_after", wait)
```

此值被设置但从未被读取。亲和渠道的 `wait` 在备选选择失败时由 `OpenCodeGoRPMError.RetryAfter` 独立计算。此 key 可能是开发中期的残留。

**建议**: 删除此行或将其用于日志/调试。

### P2-2: `affinitySecret()` 每次调用 `os.Getenv` (P3)

**文件**: [`common/internal_affinity.go:14`](file:///G:/code/gemini30/new-api/common/internal_affinity.go#L14)

```go
func affinitySecret() []byte {
    if value := strings.TrimSpace(os.Getenv("AFFINITY_SECRET")); value != "" {
        return []byte(value)
    }
    return []byte(CryptoSecret)
}
```

项目中 `CryptoSecret` 在 `init()` 中一次性读取并缓存。`affinitySecret()` 每次调用都读取 env，与项目模式不一致。虽然 `os.Getenv` 是线程安全的，但有轻微性能开销和模式不一致。

### P2-3: `opencodego_rpm_limit` 缺少上限校验 (P2)

**文件**: [`relaykit/dto/channel_settings.go:100–101`](file:///G:/code/gemini30/new-api/relaykit/dto/channel_settings.go#L100-L101)

仅校验 `< 0`，未设上限。管理员可配置极高值（如 `MaxInt`），有效禁用 RPM 保护。

**建议**: 增加合理上限校验（如 ≤ 50000）。

### P2-4: `GetOpenCodeGoRPMStatuses` 中 `cancel()` 位置 (P3)

**文件**: [`opencodego_rpm.go:131–145`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L131-L145)

```go
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
// ... 多个 Redis 调用 ...
cancel()  // L145: 在循环体末尾显式调用
```

如果未来添加 `continue` 或 `break`，`cancel()` 可能泄漏。建议改为 `defer cancel()`，但需注意循环内 `defer` 的行为 — 更安全的做法是提取为独立函数。

---

## 测试覆盖评估

### 已覆盖的关键场景 ✅

| 场景 | 测试文件 |
|------|----------|
| HMAC 签名/验证 + 篡改/版本/长度拒绝 | [`common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/common/internal_affinity_test.go) |
| 按 token 命名空间隔离 | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| 来源优先级 (session > metadata > fallback) | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| 二进制媒体排除 | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| Responses 工具输出跳过 | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| 稳定源字节限制 | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| 添加内部 Header 保留原 Override | [`relay/common/internal_affinity_test.go`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity_test.go) |
| 无效签名回退旧规则 | [`service/channel_affinity_template_test.go`](file:///G:/code/gemini30/new-api/service/channel_affinity_template_test.go) |
| 迁移成功后才更新映射 | [`service/channel_affinity_template_test.go`](file:///G:/code/gemini30/new-api/service/channel_affinity_template_test.go) |
| RPM burst 耗尽后拒绝 | [`service/opencodego_rpm_test.go`](file:///G:/code/gemini30/new-api/service/opencodego_rpm_test.go) |
| Redis 不可用时 fail-open | [`service/opencodego_rpm_test.go`](file:///G:/code/gemini30/new-api/service/opencodego_rpm_test.go) |
| 上游 429 冷却 + 默认冷却时间 | [`service/opencodego_rpm_test.go`](file:///G:/code/gemini30/new-api/service/opencodego_rpm_test.go) |
| 自动组跨组回退 | [`service/channel_select_auto_groups_test.go`](file:///G:/code/gemini30/new-api/service/channel_select_auto_groups_test.go) |
| 所有组饱和返回最短等待时间 | [`service/channel_select_auto_groups_test.go`](file:///G:/code/gemini30/new-api/service/channel_select_auto_groups_test.go) |
| OpenCodeGo 429 不永久禁用 | [`controller/relay_opencodego_test.go`](file:///G:/code/gemini30/new-api/controller/relay_opencodego_test.go) |
| Codex CLI pass_headers 端到端 | [`service/channel_affinity_template_test.go`](file:///G:/code/gemini30/new-api/service/channel_affinity_template_test.go) |

### 测试质量 ✅

- 全部使用 `testify/require` + `testify/assert`
- Redis mock 使用 `miniredis`（确定性，无需活 Redis）
- 数据库 mock 使用内存 SQLite
- 全部使用 `t.Cleanup` 清理全局状态
- 表驱动测试用于多场景验证

### 缺失的高价值测试（与文档 §14 一致）

| 缺失测试 | 风险 |
|----------|------|
| 多进程/节点 Token Bucket 一致性 | 时钟偏差影响 |
| 大型 tools/超长 Responses 的内存基准 | `max_source_bytes` 前的序列化 |
| 上游 429 的 HTTP Date 格式端到端解析 | 仅有单元级 `strconv.Atoi` |
| `SwitchOnSuccess=true` 行为 | 成功切换渠道的覆盖 |
| 中间件层集成测试 | Distributor + 亲和 + RPM 完整流 |
| `trusted_internal` 缓存统计归类 | 管理 UI 准确性 |

---

## 发现汇总表

| ID | 严重性 | 类别 | 文件 | 描述 |
|----|--------|------|------|------|
| F1 | **P1** | 部署安全 | [`relay/common/internal_affinity.go:99`](file:///G:/code/gemini30/new-api/relay/common/internal_affinity.go#L99) | `stableRequestFingerprint` 使用 `CryptoSecret`，与 `AFFINITY_SECRET` 形成隐性耦合。多节点部署必须同时统一两个密钥 |
| F2 | **P1** | 正确性 | [`opencodego_rpm.go:96`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L96) | Token Bucket Lua 脚本使用应用节点时间，多节点时钟偏差影响令牌补充准确性 |
| F3 | P2 | 代码清洁 | [`distributor.go:139`](file:///G:/code/gemini30/new-api/middleware/distributor.go#L139) | Dead context key `opencodego_rpm_retry_after` 从未读取 |
| F4 | P2 | 行为差异 | [`ability.go:148–196`](file:///G:/code/gemini30/new-api/model/ability.go#L148-L196) | DB 选择路径排除后不降级优先级，与内存缓存路径行为不同 |
| F5 | P2 | 日志 | [`opencodego_rpm.go:86–99`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L86-L99) | Redis 故障时 `SysError` 每请求打印，无频率限制 |
| F6 | P2 | 校验 | [`channel_settings.go:100`](file:///G:/code/gemini30/new-api/relaykit/dto/channel_settings.go#L100) | `opencodego_rpm_limit` 缺少上限约束 |
| F7 | P2 | 功能 | [`channel_affinity.go:155–190`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L155-L190) | `trusted_internal` 缓存条目在统计中归为 Unknown |
| F8 | P2 | 设计 | [`channel_affinity.go:569–616`](file:///G:/code/gemini30/new-api/service/channel_affinity.go#L569-L616) | 内部亲和依赖旧 `enabled` 总开关，解耦关系不清晰 |
| F9 | P3 | 性能 | [`internal_affinity.go:14`](file:///G:/code/gemini30/new-api/common/internal_affinity.go#L14) | `affinitySecret()` 每次调用 `os.Getenv`，未缓存 |
| F10 | P3 | 精度 | [`opencodego_rpm.go:93`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L93) | `time.Now()` 调用两次（minuteKey 和 ARGV），可能跨分钟边界 |
| F11 | P3 | 健壮性 | [`opencodego_rpm.go:131`](file:///G:/code/gemini30/new-api/service/opencodego_rpm.go#L131) | `cancel()` 显式调用在循环体末尾，添加 `continue/break` 时可能泄漏 |
| F12 | P3 | 防御性 | [`api_request.go:572–600`](file:///G:/code/gemini30/new-api/relay/channel/api_request.go#L572-L600) | `DoTaskApiRequest` 未显式删除内部亲和 Header（当前无泄露风险但缺少防御层） |

---

## 灰度部署建议补充

基于本次 Review 的额外建议：

1. **部署前必须**: 统一所有上层节点的 `CRYPTO_SECRET` **和** 所有上下层节点的 `AFFINITY_SECRET`（F1）
2. **建议评估**: 改用 Redis `TIME` 作为 Token Bucket 时间源（F2）
3. **监控准备**: 灰度初期关注 Redis fail-open 日志频率，如有大量输出应紧急部署频率限制（F5）
4. **配置审计**: 确认所有 OpenCodeGo 渠道的 `opencodego_rpm_limit` 不超过合理上限（F6）

---

## 二次确认：最终处理决策

> 本节是对前述 Review 报告的二次代码核对结果。若本节与前面的初审严重级别或表述冲突，以本节为准。本节只决定“是否需要处理”和处理顺序，不表示这些项目已经修改完成。

### 最终结论

当前核心方案可以保留，但不建议在完成第一批项目之前全量开启。最终分为：

- **上线前必须处理：6项**；
- **正式灰度前建议处理：8项**；
- **可以暂缓：5项**；
- **无需按初审建议处理：1项**。

### A. 上线前必须处理

| 编号 | 对应发现 | 最终级别 | 确认结果 | 必须处理的原因 |
|---|---|---:|---|---|
| A1 | F1：`CRYPTO_SECRET` 与 `AFFINITY_SECRET` 隐性耦合 | P1 | 确认 | 当前界面和部署语义容易让管理员认为只需统一 `AFFINITY_SECRET`。多上层节点 `CRYPTO_SECRET` 不一致时，session、metadata和fallback键仍不一致。应让亲和指纹只依赖亲和专用密钥，或使用普通 SHA-256 后再由亲和密钥签名。 |
| A2 | F2、F10：Token Bucket使用应用节点时间 | P1 | 确认 | 共享 Redis 不等于共享时钟。多节点时间偏差会造成少补或多补令牌。Lua应直接使用 Redis `TIME`；完成后“同一次请求调用两次 `time.Now()`”也自然消失。 |
| A3 | F4：数据库选择路径与内存路径优先级降级不一致 | P1 | 确认并上调 | 初审所称“实际影响有限”不准确。`MEMORY_CACHE_ENABLED=false` 是真实部署模式；Redis正常、RPM保护开启时，DB路径会产生排除集合。高优先级账号全部饱和后，DB路径可能直接返回429，而不是尝试低优先级可用账号。必须使两条路径行为一致。 |
| A4 | F8：内部亲和隐式依赖旧 `enabled` 总开关 | P1 | 确认 | `accept_internal_key=true` 但 `enabled=false` 时内部验签、查询和成功写回均不工作，与开关文字语义不符。优先方案是把可信内部亲和从旧自定义规则总开关中解耦；若不解耦，至少必须在后端校验和前端禁用态中明确依赖。 |
| A5 | Review指南 §12.1：`max_source_bytes` 不是严格的资源上限 | P1 | 确认，初审遗漏 | 初审Q8只确认“最终Header不含原文”，但没有解决性能问题。Chat/Claude tools会先整体 Marshal，Responses提取也可能先构造大字符串。必须改为有界提取，避免为生成32 KB指纹而复制完整大型结构。 |
| A6 | F12：Task请求未显式删除内部Header | P1 | 确认并上调 | 初审“三层防御，无泄露路径”的结论过于绝对。即使Task适配器当前不生成该Header，客户端仍能伪造同名Header，未来的通配或自定义Header逻辑也可能带入。原方案把“不得透传到任何最终上游”定义为不可关闭的安全行为，因此Task路径也应显式删除并补测试。 |

#### A组验收条件

1. 仅固定相同 `AFFINITY_SECRET`，即使测试中人为改变 `CryptoSecret`，同一请求仍生成相同内部键。
2. Token Bucket Lua不再接收应用时间参数；两个客户端连接同一Redis时共享统一时间源。
3. `MemoryCacheEnabled=true/false` 两种路径均通过“高优先级饱和、低优先级可用”的同一行为测试。
4. `enabled=false, accept_internal_key=true` 的行为有明确测试；如果选择解耦，内部亲和仍应查询和成功写回，旧规则保持关闭。
5. 大型tools和Responses输入的稳定指纹提取不整体 Marshal/拼接；达到上限后停止遍历。
6. Task上游请求无论客户端是否传入 `X-NewAPI-Affinity-Key`，最终请求Header中都不存在该字段。

### B. 正式灰度前建议处理

| 编号 | 对应发现 | 最终级别 | 确认结果 | 建议处理 |
|---|---|---:|---|---|
| B1 | F5：Redis故障日志每请求输出 | P2 | 确认 | 对Redis不可用、Eval失败和返回格式异常分别做限频或采样，保留首次及周期性告警。fail-open行为不变。 |
| B2 | F7：`trusted_internal` 被统计为 `Unknown` | P2 | 确认 | 把它作为内建规则加入统计，并支持按该规则清理，否则灰度期亲和缓存统计不可用。 |
| B3 | Q12：RPM状态查询Redis N+1 | P2 | 确认，初审汇总表遗漏 | RPM保护关闭时不查询；开启时使用Pipeline，并限制或分页渠道数量。 |
| B4 | Q12：进程内指标在多节点下易误读 | P2 | 确认 | 最低要求是在API和界面明确标注“当前节点、重启清零”；更完整方案是写入Redis并聚合。 |
| B5 | F6：RPM配置缺少上限和组合约束 | P2 | 确认 | 不建议直接照搬初审的固定 `50000`。应按已知账号硬限制制定产品边界，并同时校验全局RPM、渠道覆盖值、burst、cooldown；至少阻止burst明显大于有效RPM及非正全局值静默fail-open。 |
| B6 | `overload_policy` 是无实际分支的配置 | P2 | 确认，初审汇总表遗漏 | 当前只有 `availability_first`。应删除误导性配置/界面，或真正实现并验证其他策略；不能保留看似可配置但后端不读取的字段。 |
| B7 | F3：`opencodego_rpm_retry_after` 是dead context key | P3 | 确认 | 删除即可；如果需要用于日志，应增加明确读取点和测试，不能只写不读。 |
| B8 | 缺少OpenCodeGo适配器429解析的端到端测试 | P2 | 确认 | 现有服务测试覆盖“建立冷却”，但未直接覆盖适配器对整数秒、HTTP Date和无效值的解析及所有协议入口。 |

#### B组验收条件

- Redis故障高QPS下日志数量受控，但状态和告警仍可见；
- 管理统计能区分可信内部缓存，不再归入Unknown；
- RPM状态读取使用Pipeline，关闭保护时不产生无意义的逐渠道Redis查询；
- 配置保存接口拒绝危险或自相矛盾的RPM参数，并覆盖前后端提示；
- 适配器429测试验证整数、HTTP Date、缺失/非法值和60秒上限；
- 若删除 `overload_policy`，配置兼容旧保存值且升级不报错。

### C. 可以暂缓

| 编号 | 对应项 | 最终判断 | 原因 |
|---|---|---|---|
| C1 | 首次并发同键发生短暂缓存分裂 | 接受现状 | 最终会last-writer-wins收敛，不影响正确性。除非实测冷启动缓存损失明显，否则不引入覆盖整个上游请求的分布式锁。可后续评估短期claim或确定性选择。 |
| C2 | 自然分钟计数不是滚动60秒 | 可暂缓 | 令牌桶本身决定保护正确性，该计数仅用于展示。界面必须称为“当前自然分钟”，不能宣称滚动一分钟。 |
| C3 | F9：`affinitySecret()` 每请求读取env | 可随A1一起处理 | 单独性能影响很小。若A1引入初始化后的亲和密钥快照，可顺带消除；不值得单独改动。 |
| C4 | 下层再次执行Claude/Codex旧模板 | 当前无需修改 | 标准拓扑由上层生成并保留Header Override。只有确认存在“上层不补、必须由下层补”的部署需求时，才实现内部路由元数据与旧模板的独立合并。 |
| C5 | 指标改成真正滚动窗口和长期持久化 | 可暂缓 | 先完成B4的节点范围标识即可。是否需要长期趋势应交由外部监控系统决定。 |

### D. 无需按初审建议处理

| 编号 | 对应发现 | 最终判断 | 依据 |
|---|---|---|---|
| D1 | F11：循环中的 `cancel()` 应改为 `defer cancel()` | 不采纳 | 当前每次循环末尾显式调用 `cancel()`，不会累积到函数返回。直接在循环中使用 `defer` 反而会让最多一万个context延迟释放。若B3把状态查询重构为Pipeline或独立批处理函数，context生命周期会自然简化。 |

## 对初审结论的纠正

1. **Q4/F4影响不应描述为“有限”**：内存缓存关闭并不代表Redis不可用，DB选择路径在正常生产配置中同样可能执行RPM排除。
2. **Q6不能直接下结论“无泄露路径”**：普通HTTP、Form和WebSocket已有防御，但Task路径缺少显式删除，不满足原方案的绝对安全约束。
3. **Q8只证明了机密性，没有证明资源上限**：最终Header确实不含原文，但中间Marshal、字符串拼接和内存分配仍可能随大请求增长。
4. **初审发现汇总表不完整**：遗漏了严格提取上限、RPM状态N+1、进程指标范围和dead `overload_policy`。
5. **F6不应直接采用任意上限50000**：上限必须来自OpenCodeGo实际额度和产品策略；当前已知硬限制约1600 RPM，应围绕该事实设计默认校验和显式覆盖机制。

## 推荐实施批次

```text
批次1（阻塞全量上线）
  A1 亲和密钥解耦
  A2 Redis TIME
  A3 DB/内存选择一致
  A4 内部亲和开关语义
  A5 严格有界指纹提取
  A6 Task Header防泄露

批次2（正式灰度前）
  B1 日志限频
  B2 trusted_internal统计/清理
  B3 RPM状态Pipeline
  B4 多节点指标范围说明或聚合
  B5 配置约束
  B6 overload_policy清理
  B7 dead context key清理
  B8 429适配器端到端测试

批次3（依据运行数据）
  C1 首次并发claim/确定性选择
  C2/C5 滚动和长期监控
  C4 下层旧模板合并
```

## 最终发布判断

- **仅开发/测试环境**：可以继续验证当前实现。
- **小流量、单上层节点灰度**：在固定 `CRYPTO_SECRET`、`AFFINITY_SECRET` 和共享Redis后可以进行受控验证，但必须关注DB路径、日志和统计限制。
- **多上层节点或生产全量**：完成A1–A6后再开启；完成B1–B8后更适合扩大灰度。
- **不能仅靠部署文档永久规避A1/A2/A4**：这些问题属于代码语义或分布式正确性，长期依赖人工配置容易复发。

---

## 问题来源归属确认

### 归属口径

这里的“已有问题”分成两种，不能混为一谈：

1. **已有独立缺陷**：即使没有本次 OpenCodeGo亲和/RPM功能也会发生；
2. **已有架构的集成缺口**：原代码本身可以正常运行，但本次新增功能接入时没有完整适配已有开关、选择路径或出口。

按当前工作区与基线差异核对：`common/internal_affinity*.go`、`relay/common/internal_affinity.go`、`service/opencodego_rpm.go` 均为本次新增文件；渠道排除、内部Header、可信亲和、RPM状态和跨组容量选择也均属于本次修改。因此，当前需要处理的问题中，**没有一个可以简单归类为“与本次修改无关的历史缺陷”**。

### 第一类：本次自定义功能直接造成

| 对应项 | 来源判断 | 说明 |
|---|---|---|
| A1 / F1：双密钥耦合 | 本次新增 | 稳定指纹和内部Header签名均为新功能，`CryptoSecret` 被用于指纹是本次实现选择。 |
| A2 / F2 / F10：应用时间驱动Token Bucket | 本次新增 | `service/opencodego_rpm.go` 是新增文件，Lua时间源和重复 `time.Now()` 均来自新实现。 |
| A5：稳定指纹提取不是真正有界 | 本次新增 | tools Marshal、Responses文本提取和32 KB收集器均为新功能。 |
| B1 / F5：Redis fail-open日志风暴 | 本次新增 | RPM Redis错误日志来自新增的 `TryReserveOpenCodeGoRPM`。 |
| B2 / F7：`trusted_internal` 统计为Unknown | 本次新增 | `trusted_internal` 是新规则名，新增缓存键后没有同步扩展旧统计解析。 |
| B3：RPM状态查询N+1 | 本次新增 | `GetOpenCodeGoRPMStatuses` 是新功能。 |
| B4：指标仅进程内 | 本次新增 | `common/internal_affinity_metrics.go` 是新增文件。 |
| B5 / F6：RPM配置约束不足 | 本次新增 | `opencodego_rpm_limit`、全局RPM和burst配置均由本次功能增加。 |
| B6：dead `overload_policy` | 本次新增 | 字段和界面由本次功能增加，但后端没有策略分支。 |
| B7 / F3：dead context key | 本次新增 | `opencodego_rpm_retry_after` 由本次亲和迁移逻辑写入但未读取。 |
| B8：429适配器集成测试不足 | 本次新增测试缺口 | OpenCodeGo 429冷却处理是本次增加，缺少对应适配器级测试。 |
| C1：首次同键并发短暂分裂 | 新功能固有取舍 | 亲和缓存miss与成功写回不是原子流程，是本次亲和方案产生的eventual consistency。 |
| C2 / C5：自然分钟和指标持久化限制 | 本次新增 | RPM展示计数和内部亲和指标均为本次增加。 |
| C3 / F9：每请求读取 `AFFINITY_SECRET` env | 本次新增 | `affinitySecret()` 是新函数。 |

### 第二类：已有架构被本次修改暴露出的集成缺口

这类问题使用了已有代码基础，但具体风险是本次功能接入后才出现，因此仍应由本次功能修复，不能认为是“上游已有问题、无需处理”。

| 对应项 | 已有基础 | 本次修改造成的缺口 |
|---|---|---|
| A3 / F4：DB与内存选择不一致 | 原来就有内存缓存选择和数据库选择两条路径，优先级选择方式不同 | 本次新增 `excluded` 饱和渠道排除后，只在内存路径排除前重算优先级；DB路径在确定优先级后才排除，导致RPM场景行为分叉。没有本次排除集合时，不会以当前形式暴露。 |
| A4 / F8：内部亲和受旧 `enabled` 控制 | `enabled` 原本是旧自定义亲和规则的总开关 | 本次把可信内部亲和放进同一个函数，却沿用了函数入口的旧总开关，形成未在新开关说明中体现的隐藏依赖。 |
| A6 / F12：Task出口未删除内部Header | Task请求出口原来无需认识一个尚不存在的内部Header | 本次增加了必须禁止外泄的新Header，却没有覆盖Task出口。旧代码不是历史安全漏洞，但新功能接入不完整。 |
| C4：可信内部亲和跳过下层旧模板 | 旧 Claude/Codex亲和规则和 `ParamOverrideTemplate` 已存在 | 本次可信内部规则提前返回，可能阻止下层旧模板执行。标准上下层拓扑暂不需要处理，但属于新旧功能交互。 |

### 第三类：不是实际问题

| 对应项 | 判断 |
|---|---|
| D1 / F11：循环内显式 `cancel()` | 当前实现没有泄漏，属于初审的预防性建议，不是已有问题，也不是本次功能缺陷。直接改成循环内 `defer cancel()`反而更差。 |

### 归属结论

- **直接由本次自定义修改引入：15项**；
- **由已有架构与本次功能组合产生：4项**；
- **与本次修改无关、原本就独立存在且本次只是碰巧发现：0项**；
- **确认不构成问题：1项**。

因此，A组和B组事项都应该在当前定制分支内处理。可以复用上游已有抽象，但不应等待上游项目修复，因为这些风险依赖本次新增的内部亲和Header、RPM排除和状态统计才会出现。

---

## 修复实施结果（2026-08-15）

> 本节是对原始Review结论的实施状态回填。上方原始内容保持不变，作为审计记录。详细代码入口、行为对比和验证问题见 `docs/opencodego-multi-account-affinity-rpm-fix-review.md`。

| 编号 | 最终状态 | 实施结果 |
|---|---|---|
| A1 | 已修复、已测试 | 专用亲和密钥在启动时缓存；指纹和Header签名不再直接依赖CryptoSecret |
| A2 | 已修复、已测试 | Lua改用Redis TIME；冷却值改为哨兵并只依赖PTTL |
| A3 | 已修复、已测试 | DB路径在过滤和排除后重算优先级；内存开关双路径回归通过 |
| A4 | 已修复、已测试 | `enabled`仅控制旧规则；`accept_internal_key`独立控制可信内部亲和读写 |
| A5 | 已修复、已测试 | `fp2`有界流式HMAC提取，prompt缓存键不扫描稳定指纹，大结构不整体Marshal |
| A6 | 已修复、已测试 | Task出口显式删除内部Header，并覆盖客户端伪造回归测试 |
| B1 | 已修复、已测试 | Redis fail-open告警统一30秒限频，确定性时间测试通过 |
| B2 | 已修复、已测试 | `trusted_internal`进入规则统计并支持按规则清理 |
| B3 | 已修复、已测试 | 保护关闭时跳过查询；开启时Pipeline批量读取，1000条上限和总数/截断字段已增加 |
| B4 | 已修复、已测试 | 指标明确scope=node、节点名和重启清零；UI补充来源占比、fallback和不可用状态 |
| B5 | 已修复、已测试 | 硬限制1600及全局/渠道组合校验、已有渠道冲突检查和前端安全保存顺序已实现 |
| B6 | 已修复、已测试 | `overload_policy`从Go、TS和UI删除；历史Option过滤且拒绝重新写入 |
| B7 | 已修复 | dead context key已删除 |
| B8 | 已修复、已测试（后续纠偏） | 旧实现位于适配器不可达分支；现已移至通用HTTP响应入口，三协议真实handler路径、Retry-After格式、临时冷却和不永久禁用均有回归测试 |
| C1 | 暂缓 | 接受首次并发last-writer-wins收敛，不引入分布式claim |
| C2 | 暂缓 | 继续展示当前自然分钟，Token Bucket负责实际保护 |
| C3 | 已随A1修复、已测试 | 不再每请求读取AFFINITY_SECRET环境变量 |
| C4 | 暂缓 | 下层不新增Claude/Codex旧模板合并，上层继续负责模板Header |
| C5 | 暂缓 | 指标保持节点本地，不增加每请求Redis写入 |
| D1 | 不采纳 | 原Review已确认循环内显式cancel没有泄漏；Pipeline重构后只保留单个context |

### 最终发布判断

A1–A6和B1–B8的代码修复已完成。测试报告后续又发现type 99真实429观察点和type 60错误透传缺陷，现已补充纠偏：稳定错误码为 `opencodego_rpm_soft_limit`，合法 `Retry-After` 只在最终429输出，中间429后成功不得泄漏Header。进入灰度前仍须用新Run ID完成Mock网关E2E和低RPM迁移复验。仍需接受两项已知行为：非prompt-cache-key来源在fp2升级时发生一次亲和缓存冷启动，以及首次并发同键仍采用last-writer-wins收敛。
