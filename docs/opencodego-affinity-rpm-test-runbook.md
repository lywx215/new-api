# OpenCodeGo 多账号亲和、RPM 与缓存迁移测试运行手册

## 1. 目的和边界

本手册验证 `deepseek-v4-flash` 在以下拓扑中的行为：

```text
客户 -> 上层 New API(type 60) -> 下层 New API -> OpenCodeGo 账号 A/B/C(type 99)
                                      |
                                      +-> 共享 Redis：亲和映射、Token Bucket、cooldown
```

测试分为单实例回环和双实例验收。SQLite 可完成本次运行兼容性验收；MySQL、PostgreSQL 只由代码测试覆盖。本套工具不会自动安装 Memurai、写入 OpenCodeGo 密钥，也不会在没有显式确认时运行真实高压。

所有运行数据写入 `.local-tests/opencodego-affinity-rpm/<run-id>/`，该目录已被 Git 忽略。工具只保存渠道密钥的 8 位 SHA-256 指纹，不保存 OpenCodeGo 原始密钥、管理员 PAT、亲和密钥或完整提示词。自动创建的 New API 测试 Token 临时保存在该运行目录的 `secrets.local.json`，文件不会进入报告或 Git；清理后应删除运行副本或加密归档。

## 2. 前置条件

- Windows PowerShell 7。
- Go 1.22+；脚本也会寻找 Codex 随附的 Go 运行时。
- Bun 可从 `PATH` 访问。
- 当前测试 SQLite 数据库可写，且已有实例可以停机升级。
- Memurai Developer 安装在本机，专用端口为 `127.0.0.1:6380`。
- 三个彼此独立、无生产流量的 OpenCodeGo 账号。
- 一个临时 root PAT，仅通过 `OCG_TEST_ROOT_PAT` 环境变量提供。
- 上下层一致的 `AFFINITY_SECRET`；测试用 `SESSION_SECRET`、`CRYPTO_SECRET` 也应明确设置。

推荐先在当前 PowerShell 会话设置：

```powershell
$env:OCG_TEST_ROOT_PAT = '<temporary-root-pat>'
$env:OCG_TEST_REDIS_URL = 'redis://:<test-password>@127.0.0.1:6380/0'
$env:AFFINITY_SECRET = '<at-least-32-random-bytes>'
$env:SESSION_SECRET = '<test-session-secret>'
$env:CRYPTO_SECRET = '<test-crypto-secret>'
```

不要把这些命令连同真实值保存到脚本、终端录屏或报告。

## 3. 统一入口与恢复规则

初始化并运行到第一个人工门：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815
```

恢复同一次运行：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Resume
```

执行单步：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 `
  -RunId ocg-20260815 `
  -Step redis-validation `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

会改变设置、创建对象或发送真实请求的步骤还必须带 `-Apply`。真实高压还要求：

```powershell
$env:OCG_TEST_LIVE_CONFIRM = 'ocg-20260815'
```

每步以二进制、Git commit、SQLite、URL 和 Redis 配置生成 `input_hash`。`input-baseline.txt` 中的Redis URL只保存SHA-256身份摘要，不落盘密码。相同输入且已经通过的步骤会跳过；上次为 `running` 的步骤会自动标为 `aborted`。明确决定不执行的场景标为 `skipped`，必须写明原因；关键高压场景被跳过时总体结论只能是 `PARTIAL`，不得标为通过或阻塞。高压中断后必须等待至少 70 秒，再完整重跑该场景，不允许从请求序号中间续跑。`requests.ndjson` 是请求和费用预算的权威账本，即使 `budget.json` 损坏也可重建。每个步骤的stdout和stderr保存在 `step-<name>.log`，失败报告引用该日志，不再只留下退出码。

修复后必须使用新Run ID，但真实请求/费用预算不能因此归零。初始化新Run后执行一次：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe carry-budget `
  --run-dir .\.local-tests\opencodego-affinity-rpm\<new-run-id> `
  --from-run-dir .\.local-tests\opencodego-affinity-rpm\<old-run-id>
```

工具只读取旧Run的 `state.json` 和 `budget.json`，写入新Run的 `budget-carryover.json`；旧Run保持不变。后续从新Run的NDJSON重建预算时仍会保留该累计基线。

查看下一步和状态：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe next `
  --run-dir .\.local-tests\opencodego-affinity-rpm\ocg-20260815

Get-Content .\.local-tests\opencodego-affinity-rpm\ocg-20260815\state.json
```

## 4. 步骤说明

### Step 00：资产清点

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step inventory `
  -DatabasePath .\one-api.db `
  -BinaryPath .\.local-tests\channel-billing-final\new-api.exe
```

检查 `inventory.json`、`manifest.json`、`config-before.json`。必须人工确认：三个账号独立且无生产流量、模型可调用、接受最多 10 美元和 10000 个真实请求。当前少于三个 type 99 渠道会被记录为 blocker，但不会阻止后续纯代码和 Mock 自测。

### Step 01：备份和回滚点

`upgrade` 步骤先用 SQLite `VACUUM INTO` 创建一致性备份，再复制旧二进制并生成 `rollback.ps1`：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step upgrade `
  -DatabasePath .\one-api.db `
  -BinaryPath .\.local-tests\channel-billing-final\new-api.exe
```

脚本会把新版本构建为运行目录下的 `new-api-test.exe`，但不会猜测现有进程的完整启动环境并强制替换它。管理员应停止测试实例，用原有环境加上本手册列出的 Redis/密钥变量启动该文件，确认 `/api/status` 后，将 `upgrade` 标记为通过：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe state `
  --run-dir .\.local-tests\opencodego-affinity-rpm\ocg-20260815 `
  --step upgrade --status passed
```

这是刻意保留的停机确认点，避免遗失现有实例的未记录环境变量。

若环境已明确为可抛弃、旧进程密钥无法恢复，可在停止旧PID后生成新的本地测试密钥并轮换root PAT：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe prepare-runtime-secrets `
  --run-dir .\.local-tests\opencodego-affinity-rpm\<run-id> `
  --db .\one-api.db `
  --redis-url $env:OCG_TEST_REDIS_URL `
  --apply
```

命令把 `SESSION_SECRET`、`CRYPTO_SECRET`、`AFFINITY_SECRET`、root PAT和Redis URL写入Git忽略的 `runtime-secrets.local.json`，不会打印原值；重复执行复用同一套值。随后从该文件装载环境，令 `REDIS_CONN_STRING=redis_url`，使用绝对 `SQLITE_PATH` 启动新二进制。更换亲和密钥会导致一次预期的缓存冷启动，更换Session密钥会使旧浏览器会话失效；渠道密钥仍保存在SQLite中，不会被该操作改写。

### Step 02：代码完整验证

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step code-validation
```

依次执行根模块 `go test -p 1 ./...`、relaykit 测试和独立构建、`i18n:sync`、typecheck、本次变更前端文件定向 lint、全仓 lint、生产构建和 `git diff --check`。每条命令单独保存日志；某条命令失败时仍继续执行其余检查。后端测试、relaykit独立构建、typecheck、定向 lint、生产构建或 `git diff --check` 失败均阻止升级。全仓 lint 的既有问题单独记录为基线，不掩盖本次新增错误，也不阻止其余验证执行。

### Step 03：Redis 能力验证

Memurai 安装需要管理员人工完成。配置必须绑定 `127.0.0.1:6380`、设置测试密码，并与其他环境隔离。安装后运行：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step redis-validation -RedisUrl $env:OCG_TEST_REDIS_URL
```

工具自动验证 `PING`、Lua `TIME`、`PTTL`、Pipeline、过期和 Token Bucket 探针，并只删除带本次随机探针前缀的键。不要运行 `FLUSHDB`。重启 Memurai 后再次执行该步；若输入未变但需要强制复验，使用新 Run ID，或先把该步状态改为 `aborted`。

### Step 04–06：升级、账号渠道和回环拓扑

在 UI 人工录入三个 OpenCodeGo 账号：

- type 99、启用、模型包含 `deepseek-v4-flash`；
- 三者都加入 `ocg-affinity-lower-e2e`；
- A/B/C 分别且仅分别加入 `ocg-hardlimit-a/b/c`；
- 渠道测试均成功；
- `opencodego_rpm_limit=0`，除非需要独立覆盖。

验证布局：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step verify-channels -DatabasePath .\one-api.db
```

保存 API 快照并创建隔离分组、九个测试 Token、真实 type 60 回环渠道；其中新增的两个 Token 专用于 Mock 网关 E2E 的 upper/lower 隔离分组：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step api-snapshot
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step provision-loopback -Apply
```

创建逻辑是幂等的：同名 Token 会复用并校验分组；同名 type 60 渠道会复用，并把 Base URL 更新为当前目标。发现同名对象类型或分组不安全时停止，不覆盖。

### Step 07：Mock 确定性测试

第一层是 Mock 协议契约自检，不经过 New API：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step mock-selftest
```

它验证三个独立 Mock 账号的缓存隔离、A 的第 1601 请求准确返回 429、整数与 HTTP Date 两种 `Retry-After`，并确认测试响应格式可被负载工具解析。需要单独观察 Mock 时可启动：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe mock-server `
  --listen 127.0.0.1:39001 --account a --rpm 1600 --retry-mode seconds `
  --state-file .\.local-tests\opencodego-affinity-rpm\ocg-20260815\mock-a.json
```

不要把 Mock 渠道和真实渠道放入同一个 lower/hard-limit 分组。

第二层是完整 Mock 网关 E2E：外部请求进入 live New API 的 Mock upper 分组，经 type 60 回环、签名内部Header，再由 Mock lower 分组的三个 type 99 渠道访问三个临时 Mock 账号。

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step mock-gateway-e2e -Apply -RedisUrl $env:OCG_TEST_REDIS_URL
```

该步骤验证缓存按账号隔离、迁移后冷到热、内部Header不泄漏、实际上游429建立冷却、全部账号饱和时的稳定错误码和 `Retry-After`、等待后的恢复。Mock type 99渠道只属于 `ocg-mock-lower`，type 60只属于 `ocg-mock-upper`；每次执行会刷新临时监听URL，不消耗真实账号额度。

新建或更新 Mock 渠道后，测试工具会以独立的 readiness 亲和键轮询，等待 New API 的内存渠道缓存识别新渠道；就绪后会重置三个 Mock 账号的请求数、缓存和限流窗口，并再次精确清理这些渠道的 Redis RPM 键，再开始正式场景。readiness 请求不进入正式请求账本，也不会污染正式缓存迁移结果。

### Step 08：亲和来源和签名链路

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step affinity-smoke -Apply -DatabasePath .\one-api.db
```

脚本先应用 `single-affinity` 配置，再检查同一 `prompt_cache_key` 的五次稳定命中、不同客户隔离、session 与稳定首条消息组合、伪造 Header 覆盖，并通过下层日志关联 `channel_id/key_fp/source_type`。还需人工核对日志：原始 session、用户 ID、提示词和完整内部 Header 均不得出现。`metadata.user_id` 的临时开关场景应在独立 Run ID 中进行，完成后立即恢复 false。

### Step 09：低 RPM 缓存迁移

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step cache-migration-low-rpm -Apply -DatabasePath .\one-api.db `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

脚本应用 `RPM=1, burst=4`。每个 attempt 使用独立的 `prompt_cache_key`、亲和键和报告编号；开始前只删除清单中三个真实渠道对应的Token Bucket、自然分钟计数和cooldown精确键，并清理本attempt的可信内部亲和映射，不依赖约240秒的自然恢复，也不触碰渠道、Token、分组和其他Run的审计文件。

双实例拓扑下必须对下层3001应用 `lower-low` 配置，而不是 `single-low`；`lower-low`保持 `generate_internal_key=false`，避免下层篡改上层已签名的内部亲和键：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe apply-profile `
  --run-dir <run-dir> --run-id <run-id> --base-url http://127.0.0.1:3001 `
  --profile lower-low --apply
```

双实例下运行迁移命令时还必须传入上层SQLite，使工具可将外层响应请求ID通过 `upstream_request_id` 精确关联到下层日志：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId <run-id> `
  -Step cache-migration-low-rpm -Apply `
  -DatabasePath <run-dir>\lower.db -OuterDatabasePath <run-dir>\upper.db `
  -BaseUrl http://127.0.0.1:3000 -LowerBaseUrl http://127.0.0.1:3001 `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

脚本用约7290 Token的稳定前缀发送至少13次并写出带 `attempt` 列的请求、渠道和缓存记录。缓存分类为：冷 `<10%`、部分 `10%–80%`、热 `>=80%`。验收要求至少出现三个渠道，且每个渠道第二次成功请求为热。全部饱和必须返回稳定错误码和合法 `Retry-After` Header；缺失或非法立即失败，不能从Body推测后绕过验收。脚本按Header值加1秒安全余量后补发恢复请求。无论成功或失败，都写出本attempt的CSV、部分summary和详细错误。

重点查看：

```text
A 热 -> B 首次 -> B 热 -> C 首次 -> C 热 -> 受控 429 -> 等待后恢复
```

若新账号首次请求已经热，路由可通过，但报告必须写明“上游缓存可能跨账号共享”，生产缓存损失预期随之调整。

中断后使用同一Run ID执行 `-Resume` 会产生新attempt并使用新缓存键；旧attempt记录保持不变。若跳过精确Redis清理，则在 `RPM=1, burst=4` 下必须至少等待240秒再重试。

如果任一真实账号返回套餐、周额度或余额类429，不得把它归类为1600 RPM结果，也不得用剩余两个账号代替三账号验收。将步骤标为 `needs_manual`，保留部分缓存/渠道CSV和上游错误日志；替换该渠道账号或等待额度恢复并确认单渠道测试成功后，使用新Run ID继承旧预算，再完整重跑本步骤。

### Step 10：迁移失败与 Redis 故障

这是故障注入步骤。必须确认当前 Redis 只用于测试，再执行：

1. 记录三个渠道精确 Redis 键和当前亲和映射。
2. 让备用 Mock/测试渠道返回 500，验证原映射不变；恢复后成功请求才迁移。
3. 停止 Memurai，连续请求并核对 fail-open；30 秒内只允许一条同类告警。
4. RPM 状态应显示“不可用”，不能显示伪造的负数令牌。
5. 恢复 Memurai，确认桶和亲和映射可重建，渠道没有被永久禁用。

该步骤需要外部进程的启停，编排器会停在 `needs_manual`，避免误停非测试 Redis。完成上述检查后人工标记通过，并把日志片段路径写入 `artifacts`。

### Step 11：双实例拓扑

停止单实例后，从已验证 SQLite 备份分别复制 `upper.db`、`lower.db`。不得让两个实例同时写同一个 SQLite 文件。

上层 3000：

```text
NODE_NAME=ocg-upper-test
SQLITE_PATH=<run-dir>/upper.db
generate_internal_key=true
accept_internal_key=false
rpm_guard_enabled=false
```

下层 3001：

```text
NODE_NAME=ocg-lower-test
SQLITE_PATH=<run-dir>/lower.db
generate_internal_key=false
accept_internal_key=true
rpm_guard_enabled=true
```

两者使用同一个 `AFFINITY_SECRET` 和 Redis。用 `apply-profile upper/lower` 分别写设置，再把上层 `ocg-e2e-loopback` 更新为 `http://127.0.0.1:3001`。确认：上层只增长生成指标；下层只增长验签、命中、迁移；响应为 `scope=node` 且节点名正确；Redis RPM 状态不是节点指标；日志可按请求 ID/时间关联；没有递归。

先人工停止清单中准确的单实例 PID，再由辅助脚本创建两个独立数据库、隐藏启动进程并记录 PID/Hash/日志：

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step dual-instance -Apply `
  -DatabasePath .\one-api.db `
  -BinaryPath <run-dir>\new-api-test.exe `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

该步骤自动应用 upper/lower 配置、把 type 60 指向 3001、发送一次端到端请求，并检查上下层指标中的 `scope=node` 与节点名。进程清单保存为 `dual-instance-processes.json`。只停止清单中且二进制路径仍匹配的两个 PID：

```powershell
.\scripts\opencodego-affinity-rpm-test\dual-instance.ps1 `
  -Action Stop -RunId ocg-20260815 -Apply
```

辅助脚本不会按进程名批量停止；端口已占用、目标数据库已存在或 PID 对应的二进制路径变化时会拒绝操作。邮件、对象存储、支付等额外运行变量如生产启用了，仍应由部署环境预先注入当前 PowerShell 会话。

### Step 12：真实高压确认门

先检查 `budget.json`、三个账号 Hash、目标渠道、Redis 和服务健康，再执行：

```powershell
$env:OCG_TEST_LIVE_CONFIRM = 'ocg-20260815'
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 -Step live-gate
```

确认字符串只对同一 Run ID 有效。发现非测试流量时立即取消。

### Step 13–14：真实硬限制及 429 后缓存迁移

```powershell
.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step hard-limit -Apply `
  -DatabasePath <run-dir>\lower.db `
  -BaseUrl http://127.0.0.1:3000 `
  -LowerBaseUrl http://127.0.0.1:3001 `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

脚本先在 A 建热缓存，把 A 的精确 `opencodego:rpm:<id>` 写成带 15 分钟 TTL 的错误类型以触发已设计的 Redis fail-open，然后执行 1550/58 秒基线和最多 1650、1750/58 秒阶段。每阶段之间等待 70 秒；错误类型键在每阶段前刷新。首个 429 后停止，保留 cooldown，仅删除精确错误类型键，然后验证外层会话迁移、首次缓存分类、第二次重新变热，以及 cooldown 结束后不回漂。

该命令同时完成 Step 14，并生成 `hard-limit-post429-summary.json`。如果 1750 仍没有真实 429，命令返回失败信息，但结论应人工调整为 `PARTIAL`，不得声称已验证 1600 硬限制。任何中断后立即删除精确错误类型键，并等待 70 秒再重跑：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe redis-cleanup `
  --redis-url $env:OCG_TEST_REDIS_URL --channel-ids <A-channel-id>
```

### Step 15：三客户共 4800 RPM

先对下层应用 `lower` 配置，删除三个测试渠道精确 bucket/cooldown 键。命令会等待下一个自然分钟开始：

```powershell
& .\.local-tests\opencodego-affinity-rpm\_tool\ocg-affinity-rpm-test.exe apply-profile `
  --run-dir <run-dir> --run-id ocg-20260815 --base-url http://127.0.0.1:3001 `
  --profile lower --apply

.\scripts\opencodego-affinity-rpm-test\run.ps1 -RunId ocg-20260815 `
  -Step three-customer-4800-rpm -Apply `
  -DatabasePath <run-dir>\lower.db `
  -RedisUrl $env:OCG_TEST_REDIS_URL
```

工具以 256 并发在 58 秒内均匀发送 4800 请求，校验发送速率误差不超过 1%、没有连接错误和上游 429、至少三个独立 `key_fp`、单渠道日志请求不超过 1500，并保存 Redis 快照。预期超出总软容量的部分是本地受控 429；普通单请求随后必须恢复。

### Step 16：中断恢复

仅在 Mock/低费用环境执行。分别中断 Mock、Redis、上下层和报告生成，再用 `-Resume` 恢复。检查：`running -> aborted`；高压完整重跑；不同 input hash 不复用；损坏预算从 NDJSON 重建；重复 provision 不创建重复对象。

### Step 17：清理和回滚

默认清理：恢复 `config-before.json` 中的全局设置；禁用测试 type 60 渠道和测试 Token；保留人工录入的三个真实渠道；删除 Mock 渠道；只删除列入清单的 Redis 键；撤销 PAT；运行 SQLite `integrity_check`。编排器刻意将此步保持为确认门。

完全回滚前停止两个实例和所有测试流量：

```powershell
& <run-dir>\rollback.ps1 -Confirm
```

回滚脚本不会猜测或停止进程，只负责在 `-Confirm` 后覆盖已记录的数据库/二进制。执行前必须按 `manifest.json` 中的 PID 手动停止准确的测试实例，并确认目标路径。恢复后手动启动原实例并验证 `/api/status`。

## 5. 产物和判定

核心产物：`manifest.json`、`state.json`、`budget.json`、`config-before.json`、`configuration-changes.{json,csv,md}`、`requests.ndjson`、`rpm-per-second.csv`、`channel-transitions.csv`、`cache-transitions.csv`、`redis-snapshots.json`、`summary.md`、`failures.md`、`rollback.ps1`。

- `PASS`：代码、Mock、Redis、双实例、真实 1600、429 后缓存迁移和三客户压测全部通过。
- `PARTIAL`：功能通过但真实 1600 未复现、用户决定跳过真实高压、上游不返回缓存指标或部分生产拓扑未验收。
- `FAIL`：错误迁移、缓存无法重建、向单账号穿透超过硬限制、429 永久禁用、Redis 故障导致全站失败、数据库损坏。
- `BLOCKED`：缺账号、Redis、PAT、管理员权限或真实高压确认。

缓存和 RPM 必须分别评级。路由迁移成功不能替代缓存重建证据。
