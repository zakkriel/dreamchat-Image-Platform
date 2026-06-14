// Minimal TypeScript mirrors of the existing API contract (api/openapi.yaml).
// These are intentionally partial: only the fields the playground reads/writes.
// No new contract is introduced here — these describe existing endpoints only.

export type QualityTier = 'draft' | 'standard' | 'high'
export type LatencyTier = 'fast' | 'balanced' | 'quality'
export type DeliveryMode = 'final_only' | 'preview_first'
export type FallbackPolicy = 'none' | 'compatible_only' | 'preview_allowed' | 'any_existing'
export type OwnerType = 'character' | 'place' | 'artifact'
export type AssetType =
  | 'character_portrait'
  | 'place_scene'
  | 'artifact'
  | 'expression'
  | 'angle_variant'
export type StyleMode = 'open_prompt' | 'preset_style' | 'creator_style' | 'provider_native'
export type MatchType = 'exact_match' | 'compatible_match' | 'preview_fallback' | 'generated_required'

export const QUALITY_TIERS: QualityTier[] = ['draft', 'standard', 'high']
export const LATENCY_TIERS: LatencyTier[] = ['fast', 'balanced', 'quality']
export const DELIVERY_MODES: DeliveryMode[] = ['final_only', 'preview_first']
export const FALLBACK_POLICIES: FallbackPolicy[] = [
  'none',
  'compatible_only',
  'preview_allowed',
  'any_existing',
]
export const OWNER_TYPES: OwnerType[] = ['character', 'place', 'artifact']
export const ASSET_TYPES: AssetType[] = [
  'character_portrait',
  'place_scene',
  'artifact',
  'expression',
  'angle_variant',
]
export const STYLE_MODES: StyleMode[] = [
  'open_prompt',
  'preset_style',
  'creator_style',
  'provider_native',
]

export interface StyleProfile {
  id: string
  name: string
  style_mode: StyleMode
  positive_prompt: string
  negative_prompt?: string
  default_quality_tier?: QualityTier
  status?: string
}

export interface GenerationJobAccepted {
  job_id: string
  status: string
  estimated_cost_usd?: string
  currency?: string
  cost_reservation_id?: string
  asset_pack_id?: string
}

export interface GenerationJob {
  id: string
  status: string
  job_type: string
  visual_identity_id?: string
  asset_pack_id?: string
  preview_asset_ids?: string[]
  final_asset_ids?: string[]
  error_code?: string
  error_message?: string
  retryable?: boolean
  cost_estimate_usd?: string
  actual_cost_usd?: string
  created_at: string
  updated_at: string
}

export interface VisualAsset {
  id: string
  visual_identity_id?: string
  world_id?: string
  asset_type: AssetType
  variant_key: string
  variant_family?: string
  version: number
  status: string
  low_res_url?: string
  high_res_url?: string
  thumbnail_url?: string
  thumbnail_download_url?: string
  preview_download_url?: string
  final_download_url?: string
  url_expires_at?: string
  provider_id?: string
  model_id?: string
  metadata?: Record<string, unknown>
}

export interface JobAssetsResponse {
  assets: VisualAsset[]
}

export interface AssetSearchResponse {
  match_type?: MatchType
  compatibility_score?: number
  fallback_reason?: string
  generation_recommended?: boolean
  assets: VisualAsset[]
}

export interface WebhookEndpoint {
  id: string
  url: string
  is_active: boolean
}

export interface WebhookEndpointWithSecret extends WebhookEndpoint {
  secret: string
}
