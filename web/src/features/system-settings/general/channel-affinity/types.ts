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
export interface KeySource {
  type: 'context_int' | 'context_string' | 'request_header' | 'gjson'
  key?: string
  path?: string
}

export interface AffinityRule {
  id?: number
  name: string
  model_regex: string[]
  path_regex: string[]
  user_agent_include?: string[]
  key_sources: KeySource[]
  value_regex?: string
  ttl_seconds: number
  skip_retry_on_failure: boolean
  include_using_group: boolean
  include_model_name: boolean
  include_rule_name: boolean
  param_override_template?: Record<string, unknown> | null
}

export interface CacheStats {
  enabled: boolean
  total: number
  unknown: number
  by_rule_name: Record<string, number>
  cache_capacity: number
  cache_algo: string
  internal_affinity?: {
    scope: 'node'
    node_name: string
    reset_on_restart: boolean
    generated: number
    generated_by_source: Record<string, number>
    signature_invalid: number
    affinity_lookups: number
    affinity_hits: number
    rpm_migrations: number
    upstream_429: number
    fallback_not_generated: number
  }
  opencodego_rpm?: {
    channel_id: number
    channel_name: string
    rpm_limit: number
    requests_current_minute: number
    remaining_tokens: number
    cooldown_seconds: number
  }[]
  opencodego_rpm_total?: number
  opencodego_rpm_truncated?: boolean
}

export interface ChannelAffinitySettings {
  'channel_affinity_setting.enabled': boolean
  'channel_affinity_setting.switch_on_success': boolean
  'channel_affinity_setting.keep_on_channel_disabled': boolean
  'channel_affinity_setting.max_entries': number
  'channel_affinity_setting.default_ttl_seconds': number
  'channel_affinity_setting.rules': string
  'channel_affinity_setting.accept_internal_key': boolean
  'channel_affinity_setting.generate_internal_key': boolean
  'channel_affinity_setting.use_prompt_cache_key': boolean
  'channel_affinity_setting.use_opencode_session': boolean
  'channel_affinity_setting.use_metadata_user_id': boolean
  'channel_affinity_setting.generate_fallback_key': boolean
  'channel_affinity_setting.max_source_bytes': number
  'channel_affinity_setting.affinity_ttl_seconds': number
  'channel_affinity_setting.rpm_guard_enabled': boolean
  'channel_affinity_setting.default_account_rpm': number
  'channel_affinity_setting.account_burst': number
  'channel_affinity_setting.rate_limit_cooldown_seconds': number
}
