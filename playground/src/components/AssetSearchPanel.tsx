import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { useConfig } from '../state/config'
import {
  ASSET_TYPES,
  FALLBACK_POLICIES,
  OWNER_TYPES,
  QUALITY_TIERS,
  type AssetSearchResponse,
  type AssetType,
  type FallbackPolicy,
  type OwnerType,
  type QualityTier,
} from '../api/types'
import { assetImageUrls } from '../util'
import { Button, Field, ImageGallery, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

export function AssetSearchPanel() {
  const cfg = useConfig()
  const [worldId, setWorldId] = useState('world_dev')
  const [ownerType, setOwnerType] = useState<OwnerType | ''>('')
  const [ownerId, setOwnerId] = useState('')
  const [assetType, setAssetType] = useState<AssetType | ''>('')
  const [variantKey, setVariantKey] = useState('')
  const [styleProfileId, setStyleProfileId] = useState('')
  const [quality, setQuality] = useState<QualityTier | ''>('')
  const [fallbackPolicy, setFallbackPolicy] = useState<FallbackPolicy | ''>('')
  const [result, setResult] = useState<ApiResult<AssetSearchResponse> | null>(null)

  async function search() {
    const body: Record<string, unknown> = {}
    if (worldId) body.world_id = worldId
    if (ownerType) body.owner_type = ownerType
    if (ownerId) body.owner_id = ownerId
    if (assetType) body.asset_type = assetType
    if (variantKey) body.variant_key = variantKey
    if (styleProfileId || cfg.activeStyleId) body.style_profile_id = styleProfileId || cfg.activeStyleId
    if (quality) body.quality_tier = quality
    if (fallbackPolicy) body.fallback_policy = fallbackPolicy
    setResult(await apiRequest<AssetSearchResponse>({ method: 'POST', path: '/v1/assets/search', body }))
  }

  const data = result?.data
  const galleryUrls = (data?.assets ?? []).flatMap((a) =>
    assetImageUrls(a).map((u) => ({ label: `${a.variant_key}:${u.label}`, url: u.url })),
  )

  return (
    <Panel title="6 · Asset search" subtitle="POST /v1/assets/search — retrieval-before-generation lookup.">
      <div className="grid">
        <Field label="world_id">
          <TextInput value={worldId} onChange={setWorldId} />
        </Field>
        <Field label="owner_type">
          <Select value={ownerType} options={OWNER_TYPES} onChange={setOwnerType} allowEmpty />
        </Field>
        <Field label="owner_id">
          <TextInput value={ownerId} onChange={setOwnerId} />
        </Field>
        <Field label="asset_type">
          <Select value={assetType} options={ASSET_TYPES} onChange={setAssetType} allowEmpty />
        </Field>
        <Field label="variant_key">
          <TextInput value={variantKey} onChange={setVariantKey} />
        </Field>
        <Field label="style_profile_id">
          <TextInput value={styleProfileId || cfg.activeStyleId} onChange={setStyleProfileId} placeholder="active style" />
        </Field>
        <Field label="quality_tier">
          <Select value={quality} options={QUALITY_TIERS} onChange={setQuality} allowEmpty />
        </Field>
        <Field label="fallback_policy">
          <Select value={fallbackPolicy} options={FALLBACK_POLICIES} onChange={setFallbackPolicy} allowEmpty />
        </Field>
      </div>
      <div className="row">
        <Button onClick={() => void search()}>Search assets</Button>
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
