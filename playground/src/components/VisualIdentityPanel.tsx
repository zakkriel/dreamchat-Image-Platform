import { useEffect, useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { setConfig, useConfig } from '../state/config'
import { getScenario, useScenarioSeq } from '../scenario/store'
import type { VisualIdentity } from '../api/types'
import { Button, Field, JsonBlock, Panel, StatusBanner, TextArea, TextInput } from './ui'

type Entity = 'character' | 'place'

export function VisualIdentityPanel() {
  const cfg = useConfig()
  const [entity, setEntity] = useState<Entity>('character')
  const [worldId, setWorldId] = useState('world_dev')
  const [ownerId, setOwnerId] = useState('character_play_1')
  const [displayName, setDisplayName] = useState('Playground Hero')
  const [traits, setTraits] = useState('{\n  "hair": "black",\n  "outfit": "blue cloak"\n}')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [consistencyKey, setConsistencyKey] = useState('')
  const [traitsError, setTraitsError] = useState<string | null>(null)

  const [createResult, setCreateResult] = useState<ApiResult<VisualIdentity> | null>(null)
  const [getResult, setGetResult] = useState<ApiResult<VisualIdentity> | null>(null)

  const seq = useScenarioSeq()
  useEffect(() => {
    if (seq === 0) return
    const s = getScenario()?.visualIdentity
    if (!s) return
    if (s.ownerType !== undefined) setEntity(s.ownerType)
    if (s.worldId !== undefined) setWorldId(s.worldId)
    if (s.ownerId !== undefined) setOwnerId(s.ownerId)
    if (s.displayName !== undefined) setDisplayName(s.displayName)
    if (s.canonicalVisualTraits !== undefined) {
      setTraits(JSON.stringify(s.canonicalVisualTraits, null, 2))
      setTraitsError(null)
    }
    if (s.styleProfileId !== undefined) setStyleProfileId(s.styleProfileId)
    if (s.consistencyKey !== undefined) setConsistencyKey(s.consistencyKey)
  }, [seq])

  const effectiveStyle = styleProfileId || cfg.activeStyleId
  const basePath = entity === 'character' ? 'characters' : 'places'

  function rememberActive(vi: VisualIdentity) {
    setConfig({
      activeVisualIdentityId: vi.id,
      activeVisualIdentityOwnerType: entity,
      activeVisualIdentityOwnerId: vi.owner_id,
      activeVisualIdentityWorldId: vi.world_id,
    })
  }

  async function create() {
    let parsedTraits: Record<string, unknown>
    try {
      parsedTraits = JSON.parse(traits) as Record<string, unknown>
      setTraitsError(null)
    } catch (e) {
      setTraitsError(e instanceof Error ? e.message : 'invalid JSON')
      return
    }
    const body: Record<string, unknown> = {
      world_id: worldId,
      owner_type: entity,
      owner_id: ownerId,
      display_name: displayName,
      canonical_visual_traits: parsedTraits,
      style_profile_id: effectiveStyle,
    }
    if (consistencyKey) body.consistency_key = consistencyKey
    const res = await apiRequest<VisualIdentity>({
      method: 'POST',
      path: `/v1/${basePath}/${encodeURIComponent(ownerId)}/visual-identity`,
      body,
    })
    setCreateResult(res)
    if (res.data) rememberActive(res.data)
  }

  async function fetchIdentity() {
    const res = await apiRequest<VisualIdentity>({
      method: 'GET',
      path: `/v1/${basePath}/${encodeURIComponent(ownerId)}/visual-identity`,
      query: { world_id: worldId },
    })
    setGetResult(res)
    if (res.data) rememberActive(res.data)
  }

  return (
    <Panel
      title="3 · Visual identity"
      subtitle="Create/read a character or place visual identity. Packs require an existing identity for the owner — create one here first."
    >
      <div className="row">
        <span className="field-label">entity / owner_type:</span>
        <label className="checkbox">
          <input
            type="radio"
            checked={entity === 'character'}
            onChange={() => setEntity('character')}
          />
          <span>character</span>
        </label>
        <label className="checkbox">
          <input type="radio" checked={entity === 'place'} onChange={() => setEntity('place')} />
          <span>place</span>
        </label>
      </div>

      <div className="grid">
        <Field label="world_id">
          <TextInput value={worldId} onChange={setWorldId} />
        </Field>
        <Field label={`owner_id (= ${entity}_id path)`}>
          <TextInput value={ownerId} onChange={setOwnerId} />
        </Field>
        <Field label="display_name">
          <TextInput value={displayName} onChange={setDisplayName} />
        </Field>
        <Field label="style_profile_id (active style)">
          <TextInput value={effectiveStyle} onChange={setStyleProfileId} placeholder="style id" />
        </Field>
        <Field label="consistency_key (optional)">
          <TextInput value={consistencyKey} onChange={setConsistencyKey} placeholder="optional" />
        </Field>
      </div>
      <Field label="canonical_visual_traits (JSON)">
        <TextArea value={traits} onChange={setTraits} rows={5} />
      </Field>
      {traitsError && <div className="banner banner-err">canonical_visual_traits: {traitsError}</div>}

      <div className="row">
        <Button onClick={() => void create()} disabled={!effectiveStyle}>
          POST /v1/{basePath}/{'{id}'}/visual-identity
        </Button>
        <Button variant="secondary" onClick={() => void fetchIdentity()}>
          GET /v1/{basePath}/{'{id}'}/visual-identity
        </Button>
      </div>
      {!effectiveStyle && (
        <p className="muted note">Create or select a style first (panel 2) — style_profile_id is required.</p>
      )}

      {cfg.activeVisualIdentityId && (
        <div className="job-summary">
          <span className="pill">active VI: {cfg.activeVisualIdentityId}</span>
          <span className="pill">
            {cfg.activeVisualIdentityOwnerType}:{cfg.activeVisualIdentityOwnerId}
          </span>
        </div>
      )}

      <StatusBanner result={createResult} />
      <JsonBlock label="created/updated identity" value={createResult?.data} />
      <StatusBanner result={getResult} />
      <JsonBlock label="fetched identity" value={getResult?.data} />
    </Panel>
  )
}
