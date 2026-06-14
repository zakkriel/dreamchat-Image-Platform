import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { useConfig } from '../state/config'
import {
  FALLBACK_POLICIES,
  QUALITY_TIERS,
  type AssetSearchResponse,
  type FallbackPolicy,
  type QualityTier,
} from '../api/types'
import { assetImageUrls } from '../util'
import { Button, Field, ImageGallery, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

// Retrieval owner_type is character|place only — the backend rejects artifact
// for /v1/assets/search (internal/http/handlers/assets_handler.go).
const SEARCH_OWNER_TYPES = ['character', 'place'] as const
type SearchOwnerType = (typeof SEARCH_OWNER_TYPES)[number]

export function AssetSearchPanel() {
  const cfg = useConfig()
  const [worldId, setWorldId] = useState('world_dev')
  const [ownerType, setOwnerType] = useState<SearchOwnerType>('character')
  const [visualIdentityId, setVisualIdentityId] = useState('')
  const [variantKey, setVariantKey] = useState('neutral')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [stateVersion, setStateVersion] = useState('1')
  const [quality, setQuality] = useState<QualityTier | ''>('')
  const [fallbackPolicy, setFallbackPolicy] = useState<FallbackPolicy | ''>('')
  const [result, setResult] = useState<ApiResult<AssetSearchResponse> | null>(null)

  const effectiveVI = visualIdentityId || cfg.activeVisualIdentityId
  const effectiveStyle = styleProfileId || cfg.activeStyleId
  const stateVersionNum = Number.parseInt(stateVersion, 10)

  // The handler requires all of these; guide the user before calling.
  const missing: string[] = []
  if (!worldId) missing.push('world_id')
  if (!effectiveVI) missing.push('visual_identity_id')
  if (!variantKey) missing.push('variant_key')
  if (!effectiveStyle) missing.push('style_profile_id')
  if (!Number.isInteger(stateVersionNum)) missing.push('state_version')

  function useActiveVI() {
    if (cfg.activeVisualIdentityOwnerType) setOwnerType(cfg.activeVisualIdentityOwnerType)
    if (cfg.activeVisualIdentityId) setVisualIdentityId(cfg.activeVisualIdentityId)
    if (cfg.activeVisualIdentityWorldId) setWorldId(cfg.activeVisualIdentityWorldId)
  }

  async function search() {
    const body: Record<string, unknown> = {
      world_id: worldId,
      owner_type: ownerType,
      visual_identity_id: effectiveVI,
      variant_key: variantKey,
      style_profile_id: effectiveStyle,
      state_version: stateVersionNum,
    }
    if (quality) body.quality_tier = quality
    if (fallbackPolicy) body.fallback_policy = fallbackPolicy
    setResult(await apiRequest<AssetSearchResponse>({ method: 'POST', path: '/v1/assets/search', body }))
  }

  const data = result?.data
  const galleryUrls = (data?.assets ?? []).flatMap((a) =>
    assetImageUrls(a).map((u) => ({ label: `${a.variant_key}:${u.label}`, url: u.url })),
  )

  return (
    <Panel
      title="7 · Asset search"
      subtitle="POST /v1/assets/search — retrieval-before-generation. Requires world_id, visual_identity_id, owner_type (character|place), variant_key, style_profile_id, and state_version."
    >
      <div className="row">
        <Button variant="secondary" onClick={useActiveVI} disabled={!cfg.activeVisualIdentityId}>
          Use active visual identity
        </Button>
        {cfg.activeVisualIdentityId && (
          <span className="muted">→ {cfg.activeVisualIdentityId}</span>
        )}
      </div>
      <div className="grid">
        <Field label="world_id *">
          <TextInput value={worldId} onChange={setWorldId} />
        </Field>
        <Field label="owner_type * (character|place)">
          <Select
            value={ownerType}
            options={SEARCH_OWNER_TYPES}
            onChange={(v) => v && setOwnerType(v)}
          />
        </Field>
        <Field label="visual_identity_id *">
          <TextInput value={effectiveVI} onChange={setVisualIdentityId} placeholder="vi_..." />
        </Field>
        <Field label="variant_key *">
          <TextInput value={variantKey} onChange={setVariantKey} placeholder="e.g. neutral" />
        </Field>
        <Field label="style_profile_id *">
          <TextInput value={effectiveStyle} onChange={setStyleProfileId} placeholder="active style" />
        </Field>
        <Field label="state_version * (default 1)">
          <TextInput value={stateVersion} onChange={setStateVersion} type="number" />
        </Field>
        <Field label="quality_tier (optional)">
          <Select value={quality} options={QUALITY_TIERS} onChange={setQuality} allowEmpty />
        </Field>
        <Field label="fallback_policy (optional)">
          <Select value={fallbackPolicy} options={FALLBACK_POLICIES} onChange={setFallbackPolicy} allowEmpty />
        </Field>
      </div>
      <div className="row">
        <Button onClick={() => void search()} disabled={missing.length > 0}>
          Search assets
        </Button>
        {missing.length > 0 && <span className="muted">required: {missing.join(', ')}</span>}
      </div>

      <StatusBanner result={result} />
      {data && (
        <div className="job-summary">
          {data.match_type && <span className="pill">{data.match_type}</span>}
          {typeof data.compatibility_score === 'number' && (
            <span className="pill">score {data.compatibility_score.toFixed(2)}</span>
          )}
          {data.generation_recommended !== undefined && (
            <span className="pill">generation_recommended: {String(data.generation_recommended)}</span>
          )}
          {data.fallback_reason && <span className="muted">{data.fallback_reason}</span>}
        </div>
      )}
      <ImageGallery urls={galleryUrls} />
      <JsonBlock label="search result" value={data} />
    </Panel>
  )
}
