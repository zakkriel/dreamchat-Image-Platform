import { describe, expect, it } from 'vitest'
import { importScenario } from './importScenario'
import serenExample from '../../examples/seren-recurring-character.json'

// The canonical example scenario also documented in playground/README.md.
const EXAMPLE = {
  version: 1,
  name: 'Playground smoke test',
  connection: { baseUrl: '/api' },
  style: {
    name: 'Storybook Soft',
    styleMode: 'open_prompt',
    positivePrompt: 'clean flat illustration, soft lighting, storybook',
    negativePrompt: 'harsh shadows, photorealistic',
    defaultQualityTier: 'standard',
  },
  visualIdentity: {
    ownerType: 'character',
    worldId: 'world_dev',
    ownerId: 'character_play_1',
    displayName: 'Playground Hero',
    canonicalVisualTraits: { hair: 'black', outfit: 'blue cloak' },
    consistencyKey: '',
  },
  artifact: {
    artifactId: 'artifact_play_1',
    worldId: 'world_dev',
    description: 'a brass compass resting on an old map',
    qualityTier: 'standard',
    latencyTier: 'balanced',
    deliveryMode: 'final_only',
    forceRegenerate: false,
  },
  pack: {
    character: {
      entityId: 'character_play_1',
      worldId: 'world_dev',
      packTemplate: 'character_minimal_portrait_pack',
      qualityTier: 'standard',
    },
    place: {
      entityId: 'place_play_1',
      worldId: 'world_dev',
      packTemplate: 'place_minimal_scene_pack',
      qualityTier: 'standard',
    },
  },
  assetSearch: {
    worldId: 'world_dev',
    ownerType: 'character',
    variantKey: 'neutral',
    stateVersion: 1,
  },
  webhook: { url: 'https://webhook.site/your-id' },
  admin: { jobId: 'job_play_1' },
}

describe('importScenario', () => {
  it('accepts the canonical example and reports all filled sections', () => {
    const res = importScenario(JSON.stringify(EXAMPLE))
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.sections).toEqual([
      'connection',
      'style',
      'visualIdentity',
      'artifact',
      'pack',
      'assetSearch',
      'webhook',
      'admin',
    ])
    expect(res.scenario.connection?.baseUrl).toBe('/api')
    expect(res.scenario.visualIdentity?.canonicalVisualTraits).toEqual({
      hair: 'black',
      outfit: 'blue cloak',
    })
  })

  it('accepts a minimal scenario with only some panels', () => {
    const res = importScenario(JSON.stringify({ connection: { baseUrl: '/api' } }))
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.sections).toEqual(['connection'])
  })

  it('does not include token fields unless explicitly present', () => {
    const res = importScenario(JSON.stringify({ connection: { baseUrl: '/api' } }))
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.scenario.connection).toEqual({ baseUrl: '/api' })
    expect('token' in (res.scenario.connection ?? {})).toBe(false)
  })

  it('rejects malformed JSON', () => {
    const res = importScenario('{ not valid json ]')
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors[0]).toMatch(/Invalid JSON/)
  })

  it('rejects empty input', () => {
    const res = importScenario('   ')
    expect(res.ok).toBe(false)
  })

  it('rejects unknown top-level sections', () => {
    const res = importScenario(JSON.stringify({ bogus: {}, style: { name: 'x' } }))
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('Unknown section "bogus"'))).toBe(true)
  })

  it('rejects unknown fields within a section', () => {
    const res = importScenario(JSON.stringify({ style: { nope: 1 } }))
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('style.nope'))).toBe(true)
  })

  it('rejects invalid enum values', () => {
    const res = importScenario(JSON.stringify({ style: { styleMode: 'not_a_mode' } }))
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('style.styleMode'))).toBe(true)
  })

  it('rejects wrong field types', () => {
    const res = importScenario(
      JSON.stringify({ assetSearch: { stateVersion: 'one', worldId: 5 } }),
    )
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('assetSearch.stateVersion'))).toBe(true)
    expect(res.errors.some((e) => e.includes('assetSearch.worldId'))).toBe(true)
  })

  it('accepts the committed Seren recurring-character example (BFL anchor → fal pack)', () => {
    const res = importScenario(JSON.stringify(serenExample))
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.scenario.artifact?.providerId).toBe('bfl')
    expect(res.scenario.pack?.character?.providerId).toBe('fal')
  })

  it('accepts visualIdentity.anchorAssetIds as a string array', () => {
    const res = importScenario(
      JSON.stringify({ visualIdentity: { anchorAssetIds: ['asset_a', 'asset_b'] } }),
    )
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.scenario.visualIdentity?.anchorAssetIds).toEqual(['asset_a', 'asset_b'])
  })

  it('rejects visualIdentity.anchorAssetIds that is not an array of strings', () => {
    const res = importScenario(JSON.stringify({ visualIdentity: { anchorAssetIds: [1, 2] } }))
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('visualIdentity.anchorAssetIds'))).toBe(true)
  })

  it('accepts per-request provider preference fields on artifact and pack', () => {
    const res = importScenario(
      JSON.stringify({
        artifact: { providerId: 'bfl' },
        pack: {
          character: { providerId: 'fal' },
          place: { providerId: 'fal' },
        },
      }),
    )
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.scenario.artifact?.providerId).toBe('bfl')
    expect(res.scenario.pack?.character?.providerId).toBe('fal')
    expect(res.scenario.pack?.place?.providerId).toBe('fal')
  })

  it('rejects an unknown provider preference field name', () => {
    const res = importScenario(JSON.stringify({ artifact: { provider: 'bfl' } }))
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('artifact.provider'))).toBe(true)
  })

  it('rejects a non-string provider preference value', () => {
    const res = importScenario(
      JSON.stringify({ pack: { character: { providerId: 123 } } }),
    )
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.errors.some((e) => e.includes('pack.character.providerId'))).toBe(true)
  })

  it('allows empty string for optional select enums but not strict ones', () => {
    const ok = importScenario(JSON.stringify({ artifact: { qualityTier: '' } }))
    expect(ok.ok).toBe(true)
    const bad = importScenario(JSON.stringify({ visualIdentity: { ownerType: '' } }))
    expect(bad.ok).toBe(false)
  })
})
