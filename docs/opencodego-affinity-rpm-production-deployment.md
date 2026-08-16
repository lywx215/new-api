# OpenCodeGo 多账号亲和与 RPM 保护生产部署指导

## 1. 目标拓扑

```text
客户
  -> 上层标准/定制 New API：生成签名亲和 Header
      -> type 60 New API 渠道
          -> 下层 OpenCodeGo 定制 New API：验签、亲和、RPM 保护、429 迁移
              -> type 99 账号 A
              -> type 99 账号 B
              -> type 99 账号 C
```

每个 OpenCodeGo 账号必须对应一个独立 type 99 渠道。上层不直接持有 OpenCodeGo 密钥；下层不对外暴露内部 Token。所有下层节点使用同一个 Redis 和 `AFFINITY_SECRET`。

## 2. 上线前检查

- 使用测试报告对应的同一 Git commit/二进制 Hash。
- 数据库已备份并通过完整性检查。
- Redis 支持 `TIME`、Lua、毫秒 TTL、Pipeline，并有持久化和监控。
- OpenCodeGo 账号硬限制确认为账号级 1600 RPM。
- 每个渠道的有效软 RPM 加全局 burst 不超过 1600。
- type 60 渠道只在上层分组；type 99 渠道只在下层分组，避免递归。
- `metadata.user_id` 保持关闭，除非已证明确实为会话级且已评估热点。

## 3. 密钥和环境变量

生成至少 32 字节随机 `AFFINITY_SECRET`，通过密钥管理系统分发，不写入 Compose、systemd unit、源码或日志。上下层必须完全一致；轮换会造成验签失败和缓存冷启动，因此应采用维护窗口内的整体轮换。当前协议没有双密钥验签窗口。

`AFFINITY_SECRET` 未配置时会回退到 `CRYPTO_SECRET`，生产不应依赖此回退。`CRYPTO_SECRET` 是 New API 已有的加密用途密钥，不应为了亲和而修改；`SESSION_SECRET` 用于会话安全，也不应与亲和密钥复用。

共同环境：

```text
AFFINITY_SECRET=<same-random-secret-on-upper-and-lower>
REDIS_CONN_STRING=redis://:<password>@redis.internal:6379/0
```

节点环境：

```text
# upper
NODE_NAME=ocg-upper-01

# lower
NODE_NAME=ocg-lower-01
```

## 4. Docker Compose 示例

```yaml
services:
  new-api-upper:
    image: <tested-new-api-image>
    ports: ["3000:3000"]
    env_file: ["upper.env"]
    volumes:
      - ./upper-data:/data
    restart: unless-stopped

  new-api-lower:
    image: <tested-new-api-image>
    ports: ["127.0.0.1:3001:3000"]
    env_file: ["lower.env"]
    volumes:
      - ./lower-data:/data
    restart: unless-stopped
```

`upper.env` 和 `lower.env` 由部署系统生成，权限限制为服务账号可读。不要在 Git 中提交真实密钥。生产建议上、下层使用独立数据库；SQLite 只适合单写入实例，不能让两个进程共享同一个 SQLite 文件。

## 5. Windows 裸进程示例

在受限服务账号的环境中设置变量，然后隐藏窗口启动：

```powershell
$env:NODE_NAME = 'ocg-lower-01'
$env:SQLITE_PATH = 'D:\new-api-lower\data\one-api.db'
$env:REDIS_CONN_STRING = '<from-secret-store>'
$env:AFFINITY_SECRET = '<from-secret-store>'
Start-Process -FilePath 'D:\new-api-lower\new-api.exe' `
  -WorkingDirectory 'D:\new-api-lower' `
  -WindowStyle Hidden `
  -RedirectStandardOutput 'D:\new-api-lower\logs\stdout.log' `
  -RedirectStandardError 'D:\new-api-lower\logs\stderr.log'
```

生产推荐使用 Windows Service 包装器并配置自动重启、日志轮转和受限 ACL，不建议依赖交互式 PowerShell 会话。

## 6. Linux systemd 示例

`/etc/new-api/lower.env`：

```text
NODE_NAME=ocg-lower-01
SQLITE_PATH=/var/lib/new-api-lower/one-api.db
REDIS_CONN_STRING=redis://:<password>@127.0.0.1:6379/0
AFFINITY_SECRET=<secret>
```

`/etc/systemd/system/new-api-lower.service`：

```ini
[Unit]
Description=New API OpenCodeGo lower gateway
After=network-online.target redis.service
Wants=network-online.target

[Service]
User=new-api
Group=new-api
WorkingDirectory=/opt/new-api-lower
EnvironmentFile=/etc/new-api/lower.env
ExecStart=/opt/new-api-lower/new-api
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

环境文件权限建议 `0600`，服务目录和数据库目录归服务账号所有。

## 7. 管理端配置

上层：

- 一个 type 60 渠道，Base URL 指向下层内部地址。
- Key 为下层专用 Token。
- 渠道只属于上层客户可用分组。
- `generate_internal_key=true`。
- `accept_internal_key=false`。
- `rpm_guard_enabled=false`。

下层：

- 每个账号一个 type 99 OpenCodeGo 渠道。
- 所有账号渠道支持 `deepseek-v4-flash` 并属于同一下层分组。
- `generate_internal_key=false`。
- `accept_internal_key=true`。
- `rpm_guard_enabled=true`。

共同建议值：

| 设置 | 生产值 | 说明 |
|---|---:|---|
| `enabled` | false | 只控制旧自定义规则；使用旧规则时才开启。 |
| `use_prompt_cache_key` | true | 优先使用最接近会话缓存语义的键。 |
| `use_opencode_session` | true | 与稳定指纹组合，不单独作为键。 |
| `use_metadata_user_id` | false | 通常只是用户 ID，默认不参与。 |
| `generate_fallback_key` | true | 无可靠字段时使用有界稳定指纹。 |
| `max_source_bytes` | 32768 | 限制指纹采集成本。 |
| `affinity_ttl_seconds` | 3600 | 成功渠道亲和一小时。 |
| `switch_on_success` | true | 备用渠道成功后才更新映射。 |
| `keep_on_channel_disabled` | false | 禁用渠道不继续承载亲和。 |
| `default_account_rpm` | 1450 | 为 1600 硬限制留余量。 |
| `account_burst` | 50 | 软 RPM 与 burst 合计 1500。 |
| `rate_limit_cooldown_seconds` | 10 | 无 `Retry-After` 时默认冷却。 |

渠道级 `opencodego_rpm_limit=0` 表示继承 1450。设置独立值时，`有效软RPM + 50 <= 1600`；保存 burst 前要检查所有已有渠道覆盖值。

## 8. 发布顺序

1. 备份上下层数据库和旧二进制/镜像。
2. 先升级全部下层，所有新开关保持关闭。
3. 配置共享 Redis 和一致的 `AFFINITY_SECRET`，验证 Redis 能力。
4. 开启下层 `accept_internal_key`，确认旧流量不变。
5. 开启下层 `rpm_guard_enabled`，先以 1450/50 观察。
6. 升级上层。
7. 对少量 type 60 渠道开启 `generate_internal_key`。
8. 观察验签、亲和、迁移、缓存和 429 后逐步全量。

指纹算法 `fp2` 会使 session/metadata/fallback 路径发生一次亲和键变化，产生短期缓存冷启动；`prompt_cache_key` 来源语义保持不变。混合版本虽然 Header 线格式仍为 v1，但不应长期混跑。

## 9. 监控和告警

- 内部签名失败数应为 0；非 0 先检查密钥、版本和是否有客户伪造 Header。
- Redis fail-open 必须告警，30 秒内相同错误应限频。
- 单渠道最近一分钟请求不得达到 1600。
- 实际上游 429 应接近 0；发生后核对 `Retry-After` 和不超过 60 秒的 cooldown。
- 观察软限制迁移次数、迁移前缓存比例、新渠道首次比例和第二次重建比例。
- `scope=node` 的亲和指标只代表当前进程，重启清零；RPM token/cooldown 是 Redis 共享状态。
- Redis 状态不可用时 UI 应显示“不可用”，不能把 `-1` 当成剩余令牌。
- 监控数据库锁、连接池、500、进程重启和渠道是否被意外禁用。

## 10. 故障处理

Redis 故障时系统按设计 fail-open，因此可用性保留但可能穿透上游硬限制。立即降低入口流量或临时启用外层限流，恢复 Redis 后验证 Lua、TIME、TTL 和桶状态。不要执行未知范围的 `FLUSHDB`。

真实 429 只应创建临时 cooldown，不应永久禁用渠道。若渠道状态改变，先保存日志和 Redis 快照，再人工恢复渠道，不要直接清除所有亲和缓存。

下层全部账号达到软限制时返回稳定错误码 `opencodego_rpm_soft_limit`。上层type 60必须原样保留合法的整数或HTTP Date格式 `Retry-After`；该错误禁止重试且不得永久禁用type 60或type 99渠道。若中间渠道429后备用渠道成功，最终200响应不得携带 `Retry-After`。

单一真实会话本身超过 1600 RPM 时，可用性优先会迁移，缓存必须在新账号重建；不存在同时保持账号内缓存又突破该账号硬限制的方案。

## 11. 回滚顺序

1. 关闭上层 `generate_internal_key`，停止产生新内部亲和键。
2. 暂时保留下层 `accept_internal_key`，让在途请求完成。
3. 关闭下层 `rpm_guard_enabled`。
4. 回滚上、下层二进制或镜像并验证健康。
5. 最后处理 Redis 测试键和密钥；不要先撤掉旧版本仍需要的密钥。
6. 恢复测试前配置快照，核对渠道状态和数据库完整性。

回滚后缓存会按旧路由重新预热。保留本次测试/部署的 `configuration-changes.md`、二进制 Hash、Git commit、监控截图和回滚时间，供审计和下一次发布使用。
