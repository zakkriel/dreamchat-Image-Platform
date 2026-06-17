import { useEffect, useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { useConfig } from '../state/config'
import { getScenario, useScenarioSeq } from '../scenario/store'
import {
  DELIVERY_MODES,
  LATENCY_TIERS,
  PROVIDER_IDS,
  QUALITY_TIERS,
  type DeliveryMode,
  type GenerationJobAccepted,
  type LatencyTier,
  type ProviderId,
  type QualityTier,
} from '../api/types'
import { Button, Checkbox, Field, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

export function ArtifactPanel() {
  const cfg = useConfig()
  const [artifactId, setArtifactId] = useState('artifact_play_1')
  const [worldId, setWorldId] = useState('world_dev')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [description, setDescription] = useState('a brass compass resting on an old map')
  const [quality, setQuality] = useState<QualityTier | ''>('standard')
  const [latency, setLatency] = useState<LatencyTier | ''>('balanced')
  const [deliveryMode, setDeliveryMode] = useState<DeliveryMode | ''>('final_only')
  const [providerId, setProviderId] = useState<ProviderId | ''>('')
  const [forceRegenerate, setForceRegenerate] = useState(false)
  const [idempotencyKey, setIdempotencyKey] = useState('')
  const [result, setResult] = useState<ApiResult<GenerationJobAccepted> | null>(null)

  const seq = useScenarioSeq()
  useEffect(() => {
    if (seq === 0) return
    const s = getScenario()?.artifact
    if (!s) return
    if (s.artifactId !== undefined) setArtifactId(s.artifactId)
    if (s.worldId !== undefined) setWorldId(s.worldId)
    if (s.styleProfileId !== undefined) setStyleProfileId(s.styleProfileId)
    if (s.description !== undefined) setDescription(s.description)
    if (s.qualityTier !== undefined) setQuality(s.qualityTier)
    if (s.latencyTier !== undefined) setLatency(s.latencyTier)
    if (s.deliveryMode !== undefined) setDeliveryMode(s.deliveryMode)
    if (s.providerId !== undefined) setProviderId(s.providerId as ProviderId | '')
    if (s.forceRegenerate !== undefined) setForceRegenerate(s.forceRegenerate)
    if (s.idempotencyKey !== undefined) setIdempotencyKey(s.idempotencyKey)
  }, [seq])

  const effectiveStyle = styleProfileId || cfg.activeStyleId

  async function submit() {
    const body: Record<string, unknown> = {
      world_id: worldId,
      style_profile_id: effectiveStyle,
      description,
      force_regenerate: forceRegenerate,
    }
    if (quality) body.quality_tier = quality
    if (latency) body.latency_tier = latency
    if (deliveryMode) body.delivery_mode = deliveryMode
    if (providerId) body.provider_id = providerId
    setResult(
      await apiRequest<GenerationJobAccepted>({
        method: 'POST',
        path: `/v1/artifacts/${encodeURIComponent(artifactId)}/generate`,
        body,
        idempotencyKey: idempotencyKey || undefined,
      }),
    )
  }

  return (
    <Panel
      title="4 · Artifact generation"
      subtitle="POST /v1/artifacts/{artifact_id}/generate — uses the active style unless overridden below."
    >
      <div className="grid">
        <Field label="artifact_id">
          <TextInput value={artifactId} onChange={setArtifactId} />
        </Field>
        <Field label="world_id">
          <TextInput value={worldId} onChange={setWorldId} />
        </Field>
        <Field label={`style_profile_id${styleProfileId ? '' : ' (active style)'}`}>
          <TextInput value={effectiveStyle} onChange={setStyleProfileId} placeholder="style id" />
        </Field>
        <Field label="description">
          <TextInput value={description} onChange={setDescription} />
        </Field>
        <Field label="quality_tier">
          <Select value={quality} options={QUALITY_TIERS} onChange={setQuality} allowEmpty />
        </Field>
        <Field label="latency_tier">
          <Select value={latency} options={LATENCY_TIERS} onChange={setLatency} allowEmpty />
        </Field>
        <Field label="delivery_mode">
          <Select value={deliveryMode} options={DELIVERY_MODES} onChange={setDeliveryMode} allowEmpty />
        </Field>
        <Field label="provider_id (preference)">
          <Select value={providerId} options={PROVIDER_IDS} onChange={setProviderId} allowEmpty />
        </Field>
        <Field label="idempotency key (header)">
          <TextInput value={idempotencyKey} onChange={setIdempotencyKey} placeholder="optional" />
        </Field>
      </div>
      <div className="row">
        <Checkbox label="force_regenerate" checked={forceRegenerate} onChange={setForceRegenerate} />
        <Button onClick={() => void submit()}>Generate artifact</Button>
      </div>
      <StatusBanner result={result} />
      <JsonBlock label="accepted job" value={result?.raw} />
    </Panel>
  )
}
