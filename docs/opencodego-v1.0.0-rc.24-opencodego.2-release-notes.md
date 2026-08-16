# OpenCodeGo `v1.0.0-rc.24-opencodego.2` 发布与升级说明

发布日期：2026-08-13
适用对象：部署管理员、渠道运营人员、API 接入方和计费对账人员

## 一、版本概述

本版本是基于 new-api 上游 `v1.0.0-rc.24` 后续代码（精确基线：`upstream/main@ccd535ef`）开发的 OpenCodeGo 定制版本，合并了此前的 OpenCodeGo 渠道能力，并完成以下升级：

- OpenCodeGo 渠道类型正式固定为 `99`。
- 支持 OpenAI Chat、Anthropic Messages 和 OpenAI Responses 三种上游协议。
- 完善 GPT-5.6 Luna、MiniMax、Qwen 等模型的协议选择和请求转换。
- 增加稳定前缀缓存、缓存 token 识别及计费支持。
- 增加 OpenCodeGo 19 个官方模型的默认动态定价。
- 修复跨渠道重试、VIP/特殊分组、异步任务及钱包/订阅结算。
- 管理员显式定价继续优先，并可覆盖官方默认价格。

发布地址：<https://github.com/lywx215/new-api/releases/tag/v1.0.0-rc.24-opencodego.2>

容器镜像：

```text
ghcr.io/lywx215/new-api:v1.0.0-rc.24-opencodego.2
ghcr.io/lywx215/new-api:opencodego
```

## 二、必须关注：渠道类型 99

最终渠道编号如下：

| 渠道 | 类型编号 | 说明 |
|---|---:|---|
| Sub2API | `59` | 上游正式渠道类型 |
| New API | `60` | 上游正式渠道类型 |
| OpenCodeGo | `99` | 本 Fork 的 OpenCodeGo 专用渠道类型 |
| Dummy | `100` | 系统内部占位类型 |

升级后必须遵守以下兼容边界：

1. 所有新建 OpenCodeGo 渠道必须写入 `type=99`。
2. 外部脚本、管理 API、导入文件和自动化配置必须同步改用 `99`。
3. 最终版本中提交 `type=59` 会创建 Sub2API，不会创建 OpenCodeGo。
4. 已经将 `59` 用作 Sub2API 的上游数据库，严禁执行 OpenCodeGo 的 `59→99` 数据迁移。
5. 只有确认数据库来自本 Fork，且现存全部 `type=59` 都是旧 OpenCodeGo 渠道时，才可以执行迁移。
6. 渠道类型迁移只更新 `channels.type`；渠道 ID、Key、模型、设置、额度和 abilities 关联不会改变。

### 旧版 `59=OpenCodeGo` 的迁移步骤

1. 备份数据库，并暂停渠道新增和编辑。
2. 先将全部实例升级到兼容过渡版 `v1.0.0-rc.20-opencodego.3`。
3. 在且仅在一个已摘流主实例设置：

   ```text
   MIGRATE_OPENCODEGO_59_TO_99=true
   ```

4. 启动该实例。系统会在同一事务中把旧 OpenCodeGo 渠道从 59 更新为 99，并写入：

   ```text
   Migration.OpenCodeGoChannelType99=complete
   ```

5. 确认 `type=59` 数量为 0、`type=99` 数量与迁移前旧 OpenCodeGo 数量一致。
6. 删除迁移环境变量，重启全部过渡实例，再滚动升级到本版本。
7. 全部实例稳定后再恢复渠道编辑和 Sub2API 渠道创建。

如果最终版本发现数据库仍有未标记的 `type=59` 渠道，将拒绝启动并要求管理员先确认数据来源，防止把 Sub2API 误迁移为 OpenCodeGo。

更详细的数据库迁移流程见：[OpenCodeGo 渠道类型 59→99 迁移手册](./opencodego-channel-type-migration.md)。

### 回滚限制

- 完成 `59→99` 后，只能回滚到能够识别 `99=OpenCodeGo` 的版本。
- 不得直接回滚到只认识 `59=OpenCodeGo` 的旧版本。
- 创建 Sub2API 后，任何回滚版本都必须同时理解 `59=Sub2API`、`60=New API`、`99=OpenCodeGo`。

## 三、模型协议与调用兼容

OpenCodeGo 官方 19 个模型按以下协议访问：

| 上游协议 | 模型 |
|---|---|
| Responses | `gpt-5.6-luna` |
| Anthropic Messages | `minimax-m3`、`minimax-m2.7`、`minimax-m2.5`、`qwen3.8-max`、`qwen3.7-max`、`qwen3.7-plus`、`qwen3.6-plus` |
| OpenAI Chat | `grok-4.5`、GLM、Kimi、MiMo、DeepSeek 和 `hy3` 等其余 11 个模型 |

主要变化：

- `gpt-5.6-luna` 固定使用 `/v1/responses`，不再回退到 Chat。
- 修复 `minimax-m2.5`、`qwen3.8-max` 错误使用 OpenAI Chat 的问题。
- Chat、Messages、Responses 三种客户端入口可按模型协议进行转换。
- 原生 Responses 和原生 Messages 请求尽量保持原始结构。
- OpenCodeGo 不支持 `/v1/responses/compact`；此类请求会返回明确错误。
- 开启 body pass-through 时，客户端格式必须与选定上游协议一致，否则返回 `400`，避免将错误结构发送给上游。

渠道的 `model_protocols` 现在支持 `openai`、`anthropic`、`responses`，匹配优先级为：精确模型名、通配符、内置规则、OpenAI 默认回退。

根据实测结果，本版本只进行必要的参数清理：

- `kimi-k3`、`kimi-k2.7-code`：省略非 1 的 `temperature`。
- GLM 5.1/5.2、DeepSeek V4 Pro/Flash：只省略 `top_p=0`。
- forced tool、thinking 和其他用户语义参数不会被自动删除；模型不支持时返回上游错误。

## 四、缓存支持

本版本修复了 Chat 转 Messages 时 `cache_control` 丢失的问题，文本、图片、音频、文件、视频和工具定义中的显式缓存设置会被保留。

对于 Chat → Messages 请求，OpenCodeGo 默认增加一个稳定的 5 分钟缓存断点：

1. 优先标记最后一个非空 system 内容块。
2. 没有 system 时标记最后一个工具定义。
3. 没有 system 和工具时，标记最终问题之前的稳定历史内容。
4. 不标记末尾变化问题、空文本或 thinking 内容。
5. 请求中已有任何显式 `cache_control` 时不再自动追加，用户设置的 5 分钟或 1 小时 TTL 保持不变。
6. 原生 Messages 请求不自动注入缓存断点。

如需关闭自动缓存，可在 OpenCodeGo 渠道设置中启用“禁用 OpenCodeGo 自动缓存断点”，对应配置为：

```json
{
  "disable_opencodego_auto_cache": true
}
```

缓存是否最终命中仍取决于上游模型、稳定前缀长度和服务商策略；网关不会把未命中伪装成命中。

## 五、计费变化

### 1. 官方价格只对渠道 99 生效

OpenCodeGo 19 个官方模型的默认表达式仅在请求最终通过 `ChannelTypeOpenCodeGo=99` 时生效。其他渠道即使存在同名模型，也不会借用 OpenCodeGo 官方价格。

定价优先级为：

1. 管理员显式 `ratio`。
2. 管理员显式且有效的 `tiered_expr`。
3. 最终渠道为 99 时使用 OpenCodeGo 官方默认表达式。
4. 其他情况沿用原有 ModelPrice/ModelRatio 价格。

特别说明：管理员显式价格仍按模型名称全局生效。如果管理员修改某个模型的价格，该价格会覆盖所有渠道中的同名模型，而不仅是渠道 99。

### 2. 官方价格修订

官方价格修订号为：

```text
opencodego-go-2026-08-12
```

价格单位为美元/百万 token：

| 模型 | 输入 | 输出 | Cache Read | Cache Write |
|---|---:|---:|---:|---:|
| grok-4.5 | 2 | 6 | 0.30 | 按输入价 |
| gpt-5.6-luna | 0.20 | 1.20 | 0.02 | 0.25 |
| glm-5.2 / glm-5.1 | 1.40 | 4.40 | 0.26 | 按输入价 |
| kimi-k3 | 3 | 15 | 0.30 | 按输入价 |
| kimi-k2.7-code | 0.95 | 4 | 0.19 | 按输入价 |
| kimi-k2.6 | 0.95 | 4 | 0.16 | 按输入价 |
| mimo-v2.5 | 0.14 | 0.28 | 0.0028 | 按输入价 |
| mimo-v2.5-pro | 0.435 | 0.87 | 0.003625 | 按输入价 |
| minimax-m3 | 0.30 | 1.20 | 0.06 | 按输入价 |
| minimax-m2.7 / minimax-m2.5 | 0.30 | 1.20 | 0.06 | 0.375 |
| qwen3.8-max | 2 | 6 | 0.25 | 2.50 |
| qwen3.7-max | 2.50 | 7.50 | 0.50 | 3.125 |
| qwen3.7-plus | 0.40 | 1.60 | 0.04 | 0.50 |
| qwen3.6-plus | 0.50 | 3 | 0.05 | 0.625 |
| deepseek-v4-pro | 0.435 | 0.87 | 0.003625 | 按输入价 |
| deepseek-v4-flash | 0.14 | 0.28 | 0.0028 | 按输入价 |
| hy3 | 0.14 | 0.58 | 0.035 | 按输入价 |

长上下文价格：

- GPT-5.6 Luna：超过 272K token 后，输入/输出/Read/Write 为 `0.40/1.80/0.04/0.50`。
- Qwen3.7 Plus：超过 256K token 后为 `1.20/4.80/0.12/1.50`。
- Qwen3.6 Plus：超过 256K token 后为 `2/6/0.20/2.50`。
- 官方未区分 Cache Write 的 5 分钟和 1 小时价格，因此两种 TTL 使用相同写入单价。

### 3. 官方定价迁移

已有 OpenCodeGo 渠道的部署在启用官方默认价格前，应备份数据库并审核原有管理员定价，然后在且仅在一个主实例设置：

```text
MIGRATE_OPENCODEGO_OFFICIAL_PRICING_V1=true
```

迁移会：

- 识别真实偏离旧内置值的管理员价格，并写入显式 `mode=ratio` 予以保护。
- 保留已有 `ratio` 和 `tiered_expr`。
- 写入 `Migration.OpenCodeGoOfficialPricingV1=opencodego-go-2026-08-12`。
- 在 JSON 损坏或表达式无效时回滚，不覆盖管理员配置。

迁移完成后应删除环境变量并重启所有实例。未完成该迁移时，已有 type=99 数据库中的官方默认价格保持未激活状态，管理员价格和旧式价格继续生效。

## 六、usage、日志和对账

内部结算现在保持上游原始 usage 语义：

- Messages 分别记录未缓存输入、Cache Read、Write 5m、Write 1h 和输出 token。
- Responses 保留 cached、cache write 和 reasoning token。
- 标准 usage 优先于 OpenCodeGo 私有 `inference-cost`；只有成功且两者都不存在时才估算 token。
- `failed`、`error`、`cancelled` 请求不会按成功请求计费。
- `incomplete` 有实际 usage 时按实际值结算。
- `inference-cost` 仅用于内部计费，不转发给 API 客户端。

消费日志增加实际渠道类型、定价来源、官方修订、表达式哈希、命中档位、缓存读写、reasoning token 和上游成本等字段，方便管理员对账。

## 七、跨渠道重试与分组倍率

- 每次跨渠道重试都会根据新选中的渠道重新解析价格。
- `99→其他渠道` 时取消官方价格，使用管理员或旧式价格。
- `其他渠道→99` 时启用对应的官方表达式。
- 预扣额度只会按需要提高，最终按成功渠道结算，多预扣部分自动退回。
- 日志记录最终成功渠道和实际价格来源。

异步任务结算修复：

- 使用任务提交时保存的有效分组倍率，包括 VIP 特殊倍率和倍率 0。
- 任务执行期间修改分组配置，不改变已提交任务价格。
- 旧任务没有倍率快照时，按用户真实所属组和任务目标组重新计算特殊倍率。
- 钱包和订阅使用同一最终 quota，支持补扣和退款。

特殊倍率现在只允许有限且不小于 0 的数值；负数、NaN、无穷、`null` 和非数字会被前后端拒绝。倍率 0 继续表示免费。

## 八、管理端变化

- 渠道编辑器支持 `responses` 协议覆盖。
- 增加 OpenCodeGo 自动缓存断点开关。
- 定价列表标记“OpenCodeGo 渠道定价”，提醒价格只适用于渠道 99。
- 系统设置显示官方计费修订、官方默认数量和管理员覆盖数量。
- 修复按 Token/按次编辑后未写入 `billing_mode=ratio`，导致管理员覆盖数量仍为 0 的问题。
- 相关界面已覆盖简体中文、繁体中文、英语、法语、日语、俄语和越南语。

## 九、升级后验收清单

升级完成后建议逐项确认：

- [ ] OpenCodeGo 渠道全部为 `type=99`。
- [ ] `type=59` 只用于 Sub2API，`type=60` 只用于 New API。
- [ ] `Migration.OpenCodeGoChannelType99=complete`。
- [ ] 如启用官方价格，`Migration.OpenCodeGoOfficialPricingV1=opencodego-go-2026-08-12`。
- [ ] 管理员显式价格仍显示为管理员覆盖，并按预期作用于同名模型。
- [ ] 未显式覆盖的官方模型仅在渠道 99 使用官方价格。
- [ ] GPT-5.6 Luna 通过 Responses 调用。
- [ ] 七个 MiniMax/Qwen Messages 模型通过 `/v1/messages` 调用。
- [ ] 缓存日志能够区分 Read、Write 5m、Write 1h 和普通输入。
- [ ] VIP、特殊倍率、钱包和订阅的最终 quota 与消费日志一致。
- [ ] 所有实例移除一次性迁移环境变量后能够正常重启。

## 十、已知边界

- 单个模型的上游 5xx、限流或临时不可用不代表网关协议转换失败，应结合直连结果判断。
- 缓存命中受上游模型、最小前缀长度和缓存策略影响，网关只能保证字段正确透传或在稳定位置添加断点。
- 管理员显式定价是全局同名模型覆盖；如需不同渠道使用不同管理员价格，应使用不同模型别名，或在后续版本引入渠道级管理员定价功能。
- 完成官方定价迁移后若回滚到不支持官方默认层的旧版本，旧版本会恢复旧式价格逻辑。回滚窗口应冻结流量并重新核对价格。
