import { useEffect, useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { useConfig } from '../state/config'
import { getScenario, useScenarioSeq } from '../scenario/store'
import { QUALITY_TIERS, type GenerationJobAccepted, type QualityTier } from '../api/types'
import { Button, Checkbox, Field, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

interface PackFormProps {
  kind: 'character' | 'place'
  defaultEntityId: string
  defaultTemplate: string
  activeStyleId: string
  activeVI: {
    ownerType: '' | 'character' | 'place'
    ownerId: string
    worldId: string
  }
}

function PackForm({ kind, defaultEntityId, defaultTemplate, activeStyleId, activeVI }: PackFormProps) {
  const [entityId, setEntityId] = useState(defaultEntityId)
  const [worldId, setWorldId] = useState('world_dev')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [packTemplate, setPackTemplate] = useState(defaultTemplate)
  const [quality, setQuality] = useState<QualityTier | ''>('standard')
  const [forceRegenerate, setForceRegenerate] = useState(false)
  const [result, setResult] = useState<ApiResult<GenerationJobAccepted> | null>(null)

  const seq = useScenarioSeq()
  useEffect(() => {
    if (seq === 0) return
    const s = getScenario()?.pack?.[kind]
    if (!s) return
    if (s.entityId !== undefined) setEntityId(s.entityId)
    if (s.worldId !== undefined) setWorldId(s.worldId)
    if (s.styleProfileId !== undefined) setStyleProfileId(s.styleProfileId)
    if (s.packTemplate !== undefined) setPackTemplate(s.packTemplate)
    if (s.qualityTier !== undefined) setQuality(s.qualityTier)
    if (s.forceRegenerate !== undefined) setForceRegenerate(s.forceRegenerate)
  }, [seq, kind])

  const effectiveStyle = styleProfileId || activeStyleId
  const idLabel = kind === 'character' ? 'character_id' : 'place_id'
  const canUseActiveVI = activeVI.ownerType === kind && activeVI.ownerId !== ''

  function useActiveVI() {
    setEntityId(activeVI.ownerId)
    if (activeVI.worldId) setWorldId(activeVI.worldId)
  }

  async function submit() {
    const body: Record<string, unknown> = {
      world_id: worldId,
      style_profile_id: effectiveStyle,
      force_regenerate: forceRegenerate,
    }
    if (packTemplate) body.pack_template = packTemplate
    if (quality) body.quality_tier = quality
    setResult(
      await apiRequest<GenerationJobAccepted>({
        method: 'POST',
        path: `/v1/${kind === 'character' ? 'characters' : 'places'}/${encodeURIComponent(entityId)}/generate-pack`,
        body,
      }),
    )
  }

  return (
    <div className="subpanel">
      <div className="row">
        <h3>{kind === 'character' ? 'Character pack' : 'Place pack'}</h3>
        <Button variant="secondary" onClick={useActiveVI} disabled={!canUseActiveVI}>
          Use active visual identity
        </Button>
        {canUseActiveVI && <span className="muted">→ {activeVI.ownerId}</span>}
      </div>
      <div className="grid">
        <Field label={`${idLabel} (must have a visual identity)`}>
          <TextInput value={entityId} onChange={setEntityId} />
        </Field>
        <Field label="world_id">
          <TextInput value={worldId} onChange={setWorldId} />
        </Field>
        <Field label="style_profile_id">
          <TextInput value={effectiveStyle} onChange={setStyleProfileId} placeholder="active style" />
        </Field>
        <Field label="pack_template">
          <TextInput value={packTemplate} onChange={setPackTemplate} />
        </Field>
        <Field label="quality_tier">
          <Select value={quality} options={QUALITY_TIERS} onChange={setQuality} allowEmpty />
        </Field>
      </div>
      <div className="row">
        <Checkbox label="force_regenerate" checked={forceRegenerate} onChange={setForceRegenerate} />
        <Button onClick={() => void submit()}>
          POST /v1/{kind === 'character' ? 'characters' : 'places'}/{'{id}'}/generate-pack
        </Button>
      </div>
      <StatusBanner result={result} />
      <JsonBlock label="accepted job" value={result?.raw} />
    </div>
  )
}

export function PackPanel() {
  const cfg = useConfig()
  const activeVI = {
    ownerType: cfg.activeVisualIdentityOwnerType,
    ownerId: cfg.activeVisualIdentityOwnerId,
    worldId: cfg.activeVisualIdentityWorldId,
  }
  return (
    <Panel title="5 · Pack generation" subtitle="Existing generate-pack endpoints for characters and places.">
      <p className="muted note">
        Packs require an <strong>existing visual identity</strong> for the owner — create one in panel 3
        first, then "Use active visual identity" to fill the id/world below. The{' '}
        <code>{idLabel(cfg.activeVisualIdentityOwnerType)}</code> must match an identity you created, or
        generation will fail to resolve the owner.
      </p>
      <PackForm
        kind="character"
        defaultEntityId="character_play_1"
        defaultTemplate="character_minimal_portrait_pack"
        activeStyleId={cfg.activeStyleId}
        activeVI={activeVI}
      />
      <PackForm
        kind="place"
        defaultEntityId="place_play_1"
        defaultTemplate="place_minimal_scene_pack"
        activeStyleId={cfg.activeStyleId}
        activeVI={activeVI}
      />
    </Panel>
  )
}

function idLabel(ownerType: '' | 'character' | 'place'): string {
  if (ownerType === 'place') return 'place_id'
  return 'character_id'
}
