import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { useConfig } from '../state/config'
import { QUALITY_TIERS, type GenerationJobAccepted, type QualityTier } from '../api/types'
import { Button, Checkbox, Field, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

interface PackFormProps {
  kind: 'character' | 'place'
  defaultEntityId: string
  defaultTemplate: string
  activeStyleId: string
}

function PackForm({ kind, defaultEntityId, defaultTemplate, activeStyleId }: PackFormProps) {
  const [entityId, setEntityId] = useState(defaultEntityId)
  const [worldId, setWorldId] = useState('world_dev')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [packTemplate, setPackTemplate] = useState(defaultTemplate)
  const [quality, setQuality] = useState<QualityTier | ''>('standard')
  const [forceRegenerate, setForceRegenerate] = useState(false)
  const [result, setResult] = useState<ApiResult<GenerationJobAccepted> | null>(null)

  const effectiveStyle = styleProfileId || activeStyleId
  const idLabel = kind === 'character' ? 'character_id' : 'place_id'

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
      <h3>{kind === 'character' ? 'Character pack' : 'Place pack'}</h3>
      <div className="grid">
        <Field label={idLabel}>
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
  return (
    <Panel title="4 · Pack generation" subtitle="Existing generate-pack endpoints for characters and places.">
      <PackForm
        kind="character"
        defaultEntityId="character_play_1"
        defaultTemplate="character_minimal_portrait_pack"
        activeStyleId={cfg.activeStyleId}
      />
      <PackForm
        kind="place"
        defaultEntityId="place_play_1"
        defaultTemplate="place_minimal_scene_pack"
        activeStyleId={cfg.activeStyleId}
      />
    </Panel>
  )
}
