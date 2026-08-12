import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  CHANNEL_TYPE_OPENCODE_GO,
  CHANNEL_TYPE_OPTIONS,
  MODEL_FETCHABLE_TYPES,
} from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToCreatePayload,
} from '../channel-form'
import { getChannelTypeConfig } from '../channel-type-config'

function openCodeGoForm() {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'OpenCodeGo production',
    type: CHANNEL_TYPE_OPENCODE_GO,
    base_url: 'https://opencode.ai/zen/go/',
    key: 'test-key',
    models: 'minimax-m3,glm-5.2',
    model_protocols: JSON.stringify({
      'minimax-*': 'anthropic',
      'glm-*': 'openai',
      'gpt-5.6-luna': 'responses',
    }),
    disable_opencodego_auto_cache: true,
  }
}

describe('OpenCodeGo channel', () => {
  test('registers only channel type 99 with the official default URL', () => {
    const options = CHANNEL_TYPE_OPTIONS.filter(
      (item) => item.label === 'OpenCodeGo'
    )

    assert.deepEqual(options, [
      { value: CHANNEL_TYPE_OPENCODE_GO, label: 'OpenCodeGo' },
    ])
    assert.equal(MODEL_FETCHABLE_TYPES.has(CHANNEL_TYPE_OPENCODE_GO), true)
    assert.equal(
      getChannelTypeConfig(CHANNEL_TYPE_OPENCODE_GO).defaultBaseUrl,
      'https://opencode.ai/zen/go'
    )
  })

  test('writes type 99 and protocol overrides into channel settings', () => {
    const form = openCodeGoForm()
    assert.equal(channelFormSchema.safeParse(form).success, true)

    const payload = transformFormDataToCreatePayload(form).channel
    assert.equal(payload.type, CHANNEL_TYPE_OPENCODE_GO)
    assert.equal(payload.base_url, 'https://opencode.ai/zen/go')
    assert.deepEqual(JSON.parse(payload.settings || '{}').model_protocols, {
      'minimax-*': 'anthropic',
      'glm-*': 'openai',
      'gpt-5.6-luna': 'responses',
    })
    assert.equal(
      JSON.parse(payload.settings || '{}').disable_opencodego_auto_cache,
      true
    )
  })

  test('allows an empty Base URL so the backend uses the official default', () => {
    const form = { ...openCodeGoForm(), base_url: '' }

    assert.equal(channelFormSchema.safeParse(form).success, true)
    assert.equal(transformFormDataToCreatePayload(form).channel.base_url, null)
  })

  test('keeps Sub2API 59 and New API 60 distinct from OpenCodeGo', () => {
    const labels = new Map(
      CHANNEL_TYPE_OPTIONS.map((option) => [option.value, option.label])
    )

    assert.equal(labels.get(59), 'Sub2API')
    assert.equal(labels.get(60), 'New API')
    assert.equal(labels.get(99), 'OpenCodeGo')
  })
})
