/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Edit, FileText, Plus, RefreshCw, Trash2, X } from 'lucide-react'
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { StatusBadge, StatusBadgeList } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import { useUpdateOption } from '../../hooks/use-update-option'
import { getCacheStats, clearAllCache, clearRuleCache } from './api'
import { RULE_TEMPLATES, cloneTemplate, makeUniqueName } from './constants'
import { RuleEditorDialog } from './rule-editor-dialog'
import type { AffinityRule, CacheStats, ChannelAffinitySettings } from './types'

function parseRules(jsonStr: string): AffinityRule[] {
  try {
    const arr = JSON.parse(jsonStr || '[]')
    if (!Array.isArray(arr)) return []
    return arr.map(
      (r: Record<string, unknown>, i: number) =>
        ({ id: i, ...r }) as AffinityRule
    )
  } catch {
    return []
  }
}

function RuleBadgeList(props: { items: string[] }) {
  return (
    <StatusBadgeList
      items={props.items}
      max={2}
      getKey={(item) => item}
      renderItem={(item) => (
        <StatusBadge
          label={item}
          variant='neutral'
          size='sm'
          copyable={false}
        />
      )}
    />
  )
}

function ChannelAffinityConfirmDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  desc: ReactNode
  handleConfirm: () => void
  destructive?: boolean
}) {
  const { t } = useTranslation()

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={props.title}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      bodyClassName='flex items-start'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            variant={props.destructive ? 'destructive' : 'default'}
            onClick={props.handleConfirm}
          >
            {t('Continue')}
          </Button>
        </>
      }
    >
      <div className='text-muted-foreground text-sm'>{props.desc}</div>
    </Dialog>
  )
}

function serializeRules(rules: AffinityRule[]): string {
  return JSON.stringify(rules.map(({ id: _, ...rest }) => rest))
}

interface Props {
  defaultValues: ChannelAffinitySettings
}

export function ChannelAffinitySection(props: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const [enabled, setEnabled] = useState(
    props.defaultValues['channel_affinity_setting.enabled']
  )
  const [switchOnSuccess, setSwitchOnSuccess] = useState(
    props.defaultValues['channel_affinity_setting.switch_on_success']
  )
  const [keepOnChannelDisabled, setKeepOnChannelDisabled] = useState(
    props.defaultValues['channel_affinity_setting.keep_on_channel_disabled']
  )
  const [maxEntries, setMaxEntries] = useState(
    props.defaultValues['channel_affinity_setting.max_entries']
  )
  const [defaultTtl, setDefaultTtl] = useState(
    props.defaultValues['channel_affinity_setting.default_ttl_seconds']
  )
  const [rules, setRules] = useState<AffinityRule[]>(() =>
    parseRules(props.defaultValues['channel_affinity_setting.rules'])
  )
  const makeInternalSettings = useCallback(
    () => ({
      accept_internal_key:
        props.defaultValues['channel_affinity_setting.accept_internal_key'] ??
        false,
      generate_internal_key:
        props.defaultValues['channel_affinity_setting.generate_internal_key'] ??
        false,
      use_prompt_cache_key:
        props.defaultValues['channel_affinity_setting.use_prompt_cache_key'] ??
        true,
      use_opencode_session:
        props.defaultValues['channel_affinity_setting.use_opencode_session'] ??
        true,
      use_metadata_user_id:
        props.defaultValues['channel_affinity_setting.use_metadata_user_id'] ??
        false,
      generate_fallback_key:
        props.defaultValues['channel_affinity_setting.generate_fallback_key'] ??
        true,
      max_source_bytes:
        props.defaultValues['channel_affinity_setting.max_source_bytes'] ??
        32768,
      affinity_ttl_seconds:
        props.defaultValues['channel_affinity_setting.affinity_ttl_seconds'] ??
        3600,
      rpm_guard_enabled:
        props.defaultValues['channel_affinity_setting.rpm_guard_enabled'] ??
        false,
      default_account_rpm:
        props.defaultValues['channel_affinity_setting.default_account_rpm'] ??
        1450,
      account_burst:
        props.defaultValues['channel_affinity_setting.account_burst'] ?? 50,
      rate_limit_cooldown_seconds:
        props.defaultValues[
          'channel_affinity_setting.rate_limit_cooldown_seconds'
        ] ?? 10,
    }),
    [props.defaultValues]
  )
  const [internalSettings, setInternalSettings] = useState(makeInternalSettings)

  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [jsonText, setJsonText] = useState(() =>
    JSON.stringify(
      parseRules(props.defaultValues['channel_affinity_setting.rules']).map(
        ({ id: _, ...r }) => r
      ),
      null,
      2
    )
  )
  const [cacheStats, setCacheStats] = useState<CacheStats | null>(null)
  const [cacheLoading, setCacheLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  const [ruleEditorOpen, setRuleEditorOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<AffinityRule | null>(null)
  const [ruleTemplateKey, setRuleTemplateKey] = useState<string | null>(null)
  const [clearAllDialogOpen, setClearAllDialogOpen] = useState(false)
  const [clearRuleName, setClearRuleName] = useState<string | null>(null)
  const [fillTemplateDialogOpen, setFillTemplateDialogOpen] = useState(false)

  useEffect(() => {
    setEnabled(props.defaultValues['channel_affinity_setting.enabled'])
    setSwitchOnSuccess(
      props.defaultValues['channel_affinity_setting.switch_on_success']
    )
    setKeepOnChannelDisabled(
      props.defaultValues['channel_affinity_setting.keep_on_channel_disabled']
    )
    setMaxEntries(props.defaultValues['channel_affinity_setting.max_entries'])
    setDefaultTtl(
      props.defaultValues['channel_affinity_setting.default_ttl_seconds']
    )
    const parsed = parseRules(
      props.defaultValues['channel_affinity_setting.rules']
    )
    setRules(parsed)
    setJsonText(
      JSON.stringify(
        parsed.map(({ id: _, ...r }) => r),
        null,
        2
      )
    )
    setInternalSettings(makeInternalSettings())
  }, [props.defaultValues, makeInternalSettings])

  const refreshCache = useCallback(async () => {
    setCacheLoading(true)
    try {
      const res = await getCacheStats()
      if (res.success) setCacheStats(res.data || null)
    } catch {
      toast.error(t('Failed to refresh cache stats'))
    } finally {
      setCacheLoading(false)
    }
  }, [t])

  useEffect(() => {
    refreshCache()
  }, [refreshCache])

  const appendCliTemplates = () => {
    const existingNames = new Set(
      rules.map((r) => (r.name || '').trim()).filter((x) => x.length > 0)
    )

    const templates = Object.values(RULE_TEMPLATES).map((tpl) => {
      const base = cloneTemplate(tpl)
      const name = makeUniqueName(existingNames, tpl.name)
      existingNames.add(name)
      return { ...base, name }
    })

    setRules((prev) =>
      [...prev, ...templates].map((r, idx) => ({ ...r, id: idx }))
    )
    toast.success(t('Templates appended'))
    setFillTemplateDialogOpen(false)
  }

  const handleFillTemplates = () => {
    if (rules.length === 0) {
      appendCliTemplates()
    } else {
      setFillTemplateDialogOpen(true)
    }
  }

  const handleSave = async () => {
    if (
      internalSettings.default_account_rpm < 1 ||
      internalSettings.account_burst < 1 ||
      internalSettings.default_account_rpm + internalSettings.account_burst >
        1600
    ) {
      toast.error(
        t(
          'Soft RPM and burst must both be at least 1, and their sum must not exceed the account hard limit of 1600.'
        )
      )
      return
    }
    if (
      internalSettings.rate_limit_cooldown_seconds < 1 ||
      internalSettings.rate_limit_cooldown_seconds > 60
    ) {
      toast.error(t('429 cooldown must be between 1 and 60 seconds.'))
      return
    }
    let rulesJson: string
    if (editMode === 'json') {
      try {
        const parsed = JSON.parse(jsonText)
        if (!Array.isArray(parsed)) {
          toast.error(t('Rules JSON must be an array'))
          return
        }
        rulesJson = JSON.stringify(parsed)
      } catch {
        toast.error(t('Invalid rules JSON format'))
        return
      }
    } else {
      rulesJson = serializeRules(rules)
    }

    setSaving(true)
    try {
      const updates: { key: string; value: string }[] = []

      if (enabled !== props.defaultValues['channel_affinity_setting.enabled']) {
        updates.push({
          key: 'channel_affinity_setting.enabled',
          value: String(enabled),
        })
      }
      if (
        switchOnSuccess !==
        props.defaultValues['channel_affinity_setting.switch_on_success']
      ) {
        updates.push({
          key: 'channel_affinity_setting.switch_on_success',
          value: String(switchOnSuccess),
        })
      }
      if (
        keepOnChannelDisabled !==
        props.defaultValues['channel_affinity_setting.keep_on_channel_disabled']
      ) {
        updates.push({
          key: 'channel_affinity_setting.keep_on_channel_disabled',
          value: String(keepOnChannelDisabled),
        })
      }
      if (
        maxEntries !==
        props.defaultValues['channel_affinity_setting.max_entries']
      ) {
        updates.push({
          key: 'channel_affinity_setting.max_entries',
          value: String(maxEntries),
        })
      }
      if (
        defaultTtl !==
        props.defaultValues['channel_affinity_setting.default_ttl_seconds']
      ) {
        updates.push({
          key: 'channel_affinity_setting.default_ttl_seconds',
          value: String(defaultTtl),
        })
      }

      for (const [name, value] of Object.entries(internalSettings)) {
        const key =
          `channel_affinity_setting.${name}` as keyof ChannelAffinitySettings
        if (value !== props.defaultValues[key]) {
          updates.push({ key, value: String(value) })
        }
      }

      const origRules = props.defaultValues['channel_affinity_setting.rules']
      const origSerialized = (() => {
        try {
          return JSON.stringify(JSON.parse(origRules || '[]'))
        } catch {
          return '[]'
        }
      })()
      if (rulesJson !== origSerialized) {
        updates.push({
          key: 'channel_affinity_setting.rules',
          value: rulesJson,
        })
      }

      if (updates.length === 0) {
        toast.info(t('No changes'))
        return
      }

      for (const u of updates) {
        // Combined RPM settings are validated on every API call. Persist reductions
        // before increases so a valid final pair is never rejected transiently.
        if (
          u.key === 'channel_affinity_setting.default_account_rpm' ||
          u.key === 'channel_affinity_setting.account_burst'
        ) {
          continue
        }
        await updateOption.mutateAsync(u)
      }
      const rpmUpdates = updates
        .filter(
          (u) =>
            u.key === 'channel_affinity_setting.default_account_rpm' ||
            u.key === 'channel_affinity_setting.account_burst'
        )
        .sort((a, b) => {
          const previousA = Number(
            props.defaultValues[a.key as keyof ChannelAffinitySettings]
          )
          const previousB = Number(
            props.defaultValues[b.key as keyof ChannelAffinitySettings]
          )
          return Number(a.value) - previousA - (Number(b.value) - previousB)
        })
      for (const u of rpmUpdates) {
        await updateOption.mutateAsync(u)
      }
      toast.success(t('Saved successfully'))
    } catch {
      toast.error(t('Failed to save'))
    } finally {
      setSaving(false)
    }
  }

  const handleRuleSave = (rule: AffinityRule) => {
    setRules((prev) => {
      const existIdx = prev.findIndex(
        (r) => r.id === rule.id || (rule.name && r.name === editingRule?.name)
      )
      if (existIdx >= 0) {
        const next = [...prev]
        next[existIdx] = { ...rule, id: existIdx }
        return next
      }
      return [...prev, { ...rule, id: prev.length }]
    })
    setEditingRule(null)
  }

  const handleDeleteRule = (idx: number) => {
    setRules((prev) =>
      prev.filter((_, i) => i !== idx).map((r, i) => ({ ...r, id: i }))
    )
    toast.success(t('Deleted successfully'))
  }

  const handleClearAll = async () => {
    const res = await clearAllCache()
    if (res.success) {
      toast.success(t('Cleared'))
      refreshCache()
    }
    setClearAllDialogOpen(false)
  }

  const handleClearRule = async () => {
    if (!clearRuleName) return
    const res = await clearRuleCache(clearRuleName)
    if (res.success) {
      toast.success(t('Cleared'))
      refreshCache()
    }
    setClearRuleName(null)
  }

  const switchToJsonMode = () => {
    setJsonText(
      JSON.stringify(
        rules.map(({ id: _, ...r }) => r),
        null,
        2
      )
    )
    setEditMode('json')
  }

  const switchToVisualMode = () => {
    try {
      const parsed = JSON.parse(jsonText)
      if (!Array.isArray(parsed)) {
        toast.error(t('Rules JSON must be an array'))
        return
      }
      setRules(
        parsed.map(
          (r: Record<string, unknown>, i: number) =>
            ({ id: i, ...r }) as AffinityRule
        )
      )
      setEditMode('visual')
    } catch {
      toast.error(t('Invalid rules JSON format'))
    }
  }

  return (
    <>
      <SettingsSection title={t('Channel Affinity')}>
        <Alert>
          <AlertDescription className='text-xs'>
            {t(
              'Channel affinity reuses the last successful channel based on keys extracted from the request context or JSON body.'
            )}
          </AlertDescription>
        </Alert>

        {/* Basic Settings */}
        <div className='grid grid-cols-1 gap-4 md:grid-cols-3'>
          <SettingsSwitchField
            checked={enabled}
            onCheckedChange={setEnabled}
            label={t('Enable custom affinity rules')}
            description={t(
              'Controls only the custom rules below. Trusted internal affinity is controlled independently by Accept internal affinity key.'
            )}
            className='py-0'
          />
          <div className='grid gap-1.5'>
            <Label>{t('Max Entries')}</Label>
            <Input
              type='number'
              min={0}
              value={maxEntries}
              onChange={(e) => setMaxEntries(Number(e.target.value))}
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Default TTL (seconds)')}</Label>
            <Input
              type='number'
              min={0}
              value={defaultTtl}
              onChange={(e) => setDefaultTtl(Number(e.target.value))}
            />
          </div>
        </div>

        <SettingsSwitchField
          checked={switchOnSuccess}
          onCheckedChange={setSwitchOnSuccess}
          label={t('Switch affinity on success')}
          description={t(
            'If the affinity channel fails and retry succeeds on another channel, update affinity to the successful channel.'
          )}
        />
        <SettingsSwitchField
          checked={keepOnChannelDisabled}
          onCheckedChange={setKeepOnChannelDisabled}
          label={t('Keep affinity when channel is disabled')}
          description={t(
            'When enabled, keep the affinity entry even if the affinity channel is disabled or no longer usable for the current group/model. Leave it off to delete the entry and select another channel.'
          )}
        />

        <Separator />

        <div className='grid gap-4'>
          <div>
            <h3 className='text-sm font-medium'>
              {t('Internal Channel Affinity')}
            </h3>
            <p className='text-muted-foreground text-xs'>
              {t(
                'The upper New API signs a compact affinity key and the lower New API verifies it before routing. Configure the same AFFINITY_SECRET on every node.'
              )}
            </p>
          </div>
          <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
            {[
              [
                'accept_internal_key',
                'Accept internal affinity key',
                'When enabled, this node verifies and uses signed internal affinity headers. When disabled, the header is ignored.',
              ],
              [
                'generate_internal_key',
                'Generate internal affinity key',
                'When enabled, requests sent through New API channels receive a signed affinity header. Ordinary upstream channels are unaffected.',
              ],
              [
                'use_prompt_cache_key',
                'Use prompt cache key',
                'When enabled, prompt_cache_key is preferred because it is usually closest to the real cache session.',
              ],
              [
                'use_opencode_session',
                'Use OpenCode session',
                'When enabled, x-opencode-session is combined with a stable request fingerprint and is never used alone.',
              ],
              [
                'use_metadata_user_id',
                'Use metadata user ID',
                'Disabled by default because metadata.user_id is often user-level. When enabled, it is still combined with a stable request fingerprint.',
              ],
              [
                'generate_fallback_key',
                'Generate fallback key',
                'When enabled, requests without a reliable session field use a stable request prefix. When disabled, those requests keep existing random routing.',
              ],
              [
                'rpm_guard_enabled',
                'Enable OpenCodeGo RPM guard',
                'When enabled, Redis protects each OpenCodeGo account with a soft RPM limit and migrates overloaded affinity sessions. When disabled, routing behavior is unchanged.',
              ],
            ].map(([name, label, description]) => (
              <SettingsSwitchField
                key={name}
                checked={Boolean(
                  internalSettings[name as keyof typeof internalSettings]
                )}
                onCheckedChange={(checked) =>
                  setInternalSettings((current) => ({
                    ...current,
                    [name]: checked,
                  }))
                }
                label={t(label)}
                description={t(description)}
              />
            ))}
          </div>
          <div className='grid grid-cols-1 gap-4 md:grid-cols-4'>
            {[
              [
                'max_source_bytes',
                'Max source bytes',
                1024,
                'Limits stable text collected for the fingerprint; it does not limit the client request body.',
              ],
              [
                'affinity_ttl_seconds',
                'Internal affinity TTL (seconds)',
                1,
                'Controls how long the lower New API keeps an affinity key mapped to an OpenCodeGo account.',
              ],
              [
                'default_account_rpm',
                'Default account RPM',
                1,
                'The soft per-account limit inherited by OpenCodeGo channels whose channel RPM is 0. Soft RPM plus burst must not exceed the account hard limit of 1600.',
              ],
              [
                'account_burst',
                'Account burst',
                1,
                'Maximum immediately available token count; this is not the requests-per-minute limit. Soft RPM plus burst must not exceed the account hard limit of 1600.',
              ],
              [
                'rate_limit_cooldown_seconds',
                '429 cooldown (seconds)',
                1,
                'Used when an upstream 429 has no Retry-After value. Upstream cooldown is capped at 60 seconds.',
              ],
            ].map(([name, label, min, description]) => (
              <div className='grid gap-1.5' key={name}>
                <Label>{t(String(label))}</Label>
                <Input
                  type='number'
                  min={Number(min)}
                  value={Number(
                    internalSettings[name as keyof typeof internalSettings]
                  )}
                  onChange={(event) =>
                    setInternalSettings((current) => ({
                      ...current,
                      [name]: Number(event.target.value),
                    }))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t(String(description))}
                </p>
              </div>
            ))}
          </div>
          <Alert>
            <AlertDescription className='text-xs'>
              {t(
                'Availability-first policy: when an account reaches its soft limit, the session moves to another account. This can cause one temporary cache miss. If every account is saturated, the gateway returns 429 with Retry-After.'
              )}
            </AlertDescription>
          </Alert>
          {cacheStats?.internal_affinity && (
            <div className='space-y-2'>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Current node metrics for {{node}}; counters reset when this process restarts.',
                  { node: cacheStats.internal_affinity.node_name || '-' }
                )}
              </p>
              <div className='grid grid-cols-2 gap-2 text-xs md:grid-cols-5'>
                <StatusBadge
                  label={`${t('Generated')}: ${cacheStats.internal_affinity.generated}`}
                  variant='neutral'
                  size='sm'
                />
                <StatusBadge
                  label={`${t('Signature failures')}: ${cacheStats.internal_affinity.signature_invalid}`}
                  variant='neutral'
                  size='sm'
                />
                <StatusBadge
                  label={`${t('Affinity hit rate')}: ${cacheStats.internal_affinity.affinity_hits}/${cacheStats.internal_affinity.affinity_lookups}`}
                  variant='neutral'
                  size='sm'
                />
                <StatusBadge
                  label={`${t('RPM migrations')}: ${cacheStats.internal_affinity.rpm_migrations}`}
                  variant='neutral'
                  size='sm'
                />
                <StatusBadge
                  label={`${t('Upstream 429')}: ${cacheStats.internal_affinity.upstream_429}`}
                  variant='neutral'
                  size='sm'
                />
                <StatusBadge
                  label={`${t('Fallback not generated')}: ${cacheStats.internal_affinity.fallback_not_generated}`}
                  variant='neutral'
                  size='sm'
                />
              </div>
              <div className='text-muted-foreground text-xs'>
                {Object.entries(
                  cacheStats.internal_affinity.generated_by_source
                )
                  .map(([source, count]) => {
                    const generated =
                      cacheStats.internal_affinity?.generated ?? 0
                    const ratio =
                      generated > 0
                        ? ((count / generated) * 100).toFixed(1)
                        : '0.0'
                    return `${source}: ${count} (${ratio}%)`
                  })
                  .join(' · ')}
              </div>
            </div>
          )}
          {cacheStats?.opencodego_rpm_truncated && (
            <Alert>
              <AlertDescription className='text-xs'>
                {t(
                  'OpenCodeGo RPM status is truncated: showing the first 1000 of {{total}} channels.',
                  { total: cacheStats.opencodego_rpm_total ?? 0 }
                )}
              </AlertDescription>
            </Alert>
          )}
          {cacheStats?.opencodego_rpm &&
            cacheStats.opencodego_rpm.length > 0 && (
              <div className='space-y-2'>
                <Label>{t('OpenCodeGo account status')}</Label>
                {cacheStats.opencodego_rpm.map((status) => (
                  <div
                    key={status.channel_id}
                    className='text-muted-foreground rounded-md border p-2 text-xs'
                  >
                    <span className='text-foreground font-medium'>
                      {status.channel_name} (#{status.channel_id})
                    </span>{' '}
                    · {t('Current minute requests')}:{' '}
                    {status.requests_current_minute}/{status.rpm_limit} ·{' '}
                    {t('Remaining tokens')}:{' '}
                    {status.remaining_tokens < 0
                      ? t('Unavailable')
                      : status.remaining_tokens.toFixed(1)}
                    {status.cooldown_seconds > 0 && (
                      <>
                        {' '}
                        · {t('Cooldown seconds')}: {status.cooldown_seconds}
                      </>
                    )}
                  </div>
                ))}
              </div>
            )}
        </div>

        <Separator />

        <SettingsPageActionsPortal>
          <Button
            variant={editMode === 'visual' ? 'default' : 'outline'}
            size='sm'
            onClick={editMode === 'json' ? switchToVisualMode : undefined}
          >
            {t('Visual')}
          </Button>
          <Button
            variant={editMode === 'json' ? 'default' : 'outline'}
            size='sm'
            onClick={editMode === 'visual' ? switchToJsonMode : undefined}
          >
            JSON
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant='outline' size='sm' />}
            >
              <Plus className='mr-1 h-3 w-3' />
              {t('Add Rule')}
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuItem
                onClick={() => {
                  setEditingRule(null)
                  setRuleTemplateKey(null)
                  setRuleEditorOpen(true)
                }}
              >
                {t('Blank Rule')}
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => {
                  setEditingRule(null)
                  setRuleTemplateKey('codexCli')
                  setRuleEditorOpen(true)
                }}
              >
                Codex CLI
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => {
                  setEditingRule(null)
                  setRuleTemplateKey('claudeCli')
                  setRuleEditorOpen(true)
                }}
              >
                Claude CLI
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Button variant='outline' size='sm' onClick={handleFillTemplates}>
            <FileText className='mr-1 h-3 w-3' />
            {t('Fill Templates')}
          </Button>
          <Button size='sm' onClick={handleSave} disabled={saving}>
            {saving ? t('Saving...') : t('Save')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            onClick={refreshCache}
            disabled={cacheLoading}
          >
            <RefreshCw
              className={`mr-1 h-3 w-3 ${cacheLoading ? 'animate-spin' : ''}`}
            />
            {t('Refresh Cache')}
          </Button>
          <Button
            variant='destructive'
            size='sm'
            onClick={() => setClearAllDialogOpen(true)}
          >
            {t('Clear All Cache')}
          </Button>
          {cacheStats && (
            <span className='text-muted-foreground text-xs'>
              {t('Cache Entries')}: {cacheStats.total} /{' '}
              {cacheStats.cache_capacity}
            </span>
          )}
        </SettingsPageActionsPortal>

        {/* Rules Table or JSON Editor */}
        {editMode === 'visual' ? (
          <StaticDataTable
            tableClassName='min-w-max'
            data={rules}
            emptyClassName='text-muted-foreground py-8'
            emptyContent={t('No rules yet')}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (rule) => rule.name || '-',
              },
              {
                id: 'model-regex',
                header: t('Model Regex'),
                cell: (rule) => (
                  <RuleBadgeList items={rule.model_regex || []} />
                ),
              },
              {
                id: 'key-sources',
                header: t('Key Sources'),
                cell: (rule) => (
                  <RuleBadgeList
                    items={(rule.key_sources || []).map(
                      (src) =>
                        `${src.type}:${src.type === 'gjson' ? src.path : src.key}`
                    )}
                  />
                ),
              },
              {
                id: 'ttl',
                header: t('TTL'),
                cell: (rule) => rule.ttl_seconds || '-',
              },
              {
                id: 'retry',
                header: t('Retry'),
                cell: (rule) => (
                  <StatusBadge
                    label={
                      rule.skip_retry_on_failure ? t('No Retry') : t('Retry')
                    }
                    variant={rule.skip_retry_on_failure ? 'danger' : 'neutral'}
                    copyable={false}
                  />
                ),
              },
              {
                id: 'scope',
                header: t('Scope'),
                cell: (rule) => {
                  const scopeItems = [
                    rule.include_using_group && t('Group'),
                    rule.include_model_name && t('Model'),
                    rule.include_rule_name && t('Rule'),
                  ].filter(Boolean) as string[]
                  if (scopeItems.length === 0) return '-'
                  return <RuleBadgeList items={scopeItems} />
                },
              },
              {
                id: 'cache',
                header: t('Cache'),
                cell: (rule) =>
                  rule.include_rule_name && cacheStats?.by_rule_name
                    ? cacheStats.by_rule_name[rule.name] || 0
                    : 'N/A',
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (rule, idx) => (
                  <div className='flex justify-end gap-1'>
                    {rule.include_rule_name && (
                      <Button
                        variant='ghost'
                        size='icon'
                        className='h-7 w-7'
                        onClick={() => setClearRuleName(rule.name)}
                        title={t('Clear cache for this rule')}
                      >
                        <X className='h-3 w-3' />
                      </Button>
                    )}
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      onClick={() => {
                        setEditingRule(rule)
                        setRuleTemplateKey(null)
                        setRuleEditorOpen(true)
                      }}
                    >
                      <Edit className='h-3 w-3' />
                    </Button>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      onClick={() => handleDeleteRule(idx)}
                    >
                      <Trash2 className='h-3 w-3' />
                    </Button>
                  </div>
                ),
              },
            ]}
          />
        ) : (
          <div className='grid gap-1.5'>
            <Label htmlFor='channel-affinity-rules-json'>
              {t('Rules JSON')}
            </Label>
            <JsonCodeEditor
              id='channel-affinity-rules-json'
              value={jsonText}
              onChange={setJsonText}
              heightClassName='h-[300px] min-h-[300px] max-h-[300px]'
            />
          </div>
        )}
      </SettingsSection>

      <RuleEditorDialog
        open={ruleEditorOpen}
        onOpenChange={setRuleEditorOpen}
        rule={editingRule}
        onSave={handleRuleSave}
        templateKey={ruleTemplateKey}
      />

      <ChannelAffinityConfirmDialog
        open={clearAllDialogOpen}
        onOpenChange={setClearAllDialogOpen}
        title={t('Confirm clearing all channel affinity cache')}
        desc={t(
          'This will delete all channel affinity cache entries still in memory.'
        )}
        handleConfirm={handleClearAll}
        destructive
      />

      {clearRuleName !== null && (
        <ChannelAffinityConfirmDialog
          open
          onOpenChange={(v) => !v && setClearRuleName(null)}
          title={t('Confirm clearing cache for this rule')}
          desc={`${t('Rule')}: ${clearRuleName}`}
          handleConfirm={handleClearRule}
          destructive
        />
      )}

      <ChannelAffinityConfirmDialog
        open={fillTemplateDialogOpen}
        onOpenChange={setFillTemplateDialogOpen}
        title={t('Fill Codex CLI / Claude CLI Templates')}
        desc={t(
          'This will append 2 template rules (Codex CLI and Claude CLI) to the existing rule list.'
        )}
        handleConfirm={appendCliTemplates}
      />
    </>
  )
}
