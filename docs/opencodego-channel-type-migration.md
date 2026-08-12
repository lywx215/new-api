# OpenCodeGo 渠道类型 59→99 迁移手册

最终渠道编号为：Sub2API `59`、New API `60`、OpenCodeGo `99`。迁移标记存储在 `options` 表，键为 `Migration.OpenCodeGoChannelType99`，完成值为 `complete`。

## 迁移前

1. 先将全部实例升级到兼容过渡版本 `v1.0.0-rc.20-opencodego.3`。该版本可读取 59 和 99，但管理端只新建 99。
2. 备份主数据库，暂停渠道新增和编辑，并记录旧数据数量：

   ```sql
   SELECT type, COUNT(*) FROM channels WHERE type IN (59, 99) GROUP BY type;
   ```

3. 确认数据库来自本 Fork，且全部 `type=59` 渠道都是 OpenCodeGo。不要对已经使用 59 表示 Sub2API 的上游数据库执行此迁移。

## 执行迁移

在且仅在一个已摘流的主节点设置：

```text
MIGRATE_OPENCODEGO_59_TO_99=true
```

启动该实例。迁移会在数据库结构迁移完成后、OptionMap 和渠道缓存初始化前运行，并在同一事务内更新渠道和写入完成标记。任何一步失败都会回滚并阻止服务启动。

确认启动日志中的迁移数量后，移除环境变量并重启该实例。再重启其余兼容过渡实例，使其重新加载渠道缓存。

## 验收

```sql
SELECT type, COUNT(*) FROM channels WHERE type IN (59, 99) GROUP BY type;
SELECT value FROM options WHERE key = 'Migration.OpenCodeGoChannelType99';
```

要求旧渠道 `type=59` 数量为 0，`type=99` 数量与迁移前的 59 数量一致，迁移标记为 `complete`。渠道 ID、Key、模型、设置、额度和 abilities 不应变化。

验收完成后可滚动升级最终版本 `v1.0.0-rc.24-opencodego.1`。最终版本中 59 只表示 Sub2API；滚动期间不要创建 Sub2API，全部实例稳定后再恢复渠道管理。

## 回滚边界

迁移完成后只能回滚到支持 `99=OpenCodeGo` 的兼容过渡版本，不能回滚到只认识 `59=OpenCodeGo` 的旧版本。创建 Sub2API 后，任何回滚目标都必须同时理解 `59=Sub2API`、`60=New API`、`99=OpenCodeGo`。
