// Typed schema for an importable playground scenario.
//
// A scenario is a plain JSON document that pre-fills the playground's panel
// forms so a predefined test case can be repeated without retyping every field.
// It is dev/local-only: scenarios are never uploaded or stored server-side, and
// importing one only fills form fields — it never auto-submits an API call.
//
// Every section is optional. Only the sections present in the document are
// applied; the rest of the playground keeps its current values. Each section
// mirrors the fields of one panel.
import type {
  DeliveryMode,
  FallbackPolicy,
  LatencyTier,
  QualityTier,
  StyleMode,
} from '../api/types'

export type PackEntity = 'character' | 'place'

export interface ConnectionScenario {
  // Token fields are intentionally optional and, in committed examples, omitted:
  // never put raw bearer tokens in a scenario file. They are honored on import
  // only when explicitly present.
  baseUrl?: string
  token?: string
  adminToken?: string
}

export interface StyleScenario {
  name?: string
  styleMode?: StyleMode
  positivePrompt?: string
  negativePrompt?: string
  defaultQualityTier?: QualityTier | ''
}

export interface VisualIdentityScenario {
  ownerType?: PackEntity
  worldId?: string
  ownerId?: string
  displayName?: string
  // Stored as a JSON object; rendered into the panel's JSON textarea.
  canonicalVisualTraits?: Record<string, unknown>
  styleProfileId?: string
  consistencyKey?: string
}

export interface ArtifactScenario {
  artifactId?: string
  worldId?: string
  styleProfileId?: string
  description?: string
  qualityTier?: QualityTier | ''
  latencyTier?: LatencyTier | ''
  deliveryMode?: DeliveryMode | ''
  // Optional per-request provider preference (provider_id). Free string so it
  // stays deployment-agnostic; the panel renders it as a select of known providers.
  providerId?: string
  forceRegenerate?: boolean
  idempotencyKey?: string
}

export interface PackEntityScenario {
  entityId?: string
  worldId?: string
  styleProfileId?: string
  packTemplate?: string
  qualityTier?: QualityTier | ''
  // Optional per-request provider preference (provider_id) for this pack kind.
  providerId?: string
  forceRegenerate?: boolean
}

export interface PackScenario {
  character?: PackEntityScenario
  place?: PackEntityScenario
}

export interface AssetSearchScenario {
  worldId?: string
  ownerType?: PackEntity
  visualIdentityId?: string
  variantKey?: string
  styleProfileId?: string
  stateVersion?: number
  qualityTier?: QualityTier | ''
  fallbackPolicy?: FallbackPolicy | ''
}

export interface WebhookScenario {
  url?: string
}

export interface AdminScenario {
  jobId?: string
}

export interface Scenario {
  // Optional metadata, ignored by panels but shown to the user.
  version?: number
  name?: string
  connection?: ConnectionScenario
  style?: StyleScenario
  visualIdentity?: VisualIdentityScenario
  artifact?: ArtifactScenario
  pack?: PackScenario
  assetSearch?: AssetSearchScenario
  webhook?: WebhookScenario
  admin?: AdminScenario
}

// Panel sections (everything except the bare metadata keys). Used both to
// reject unknown sections during validation and to report which panels filled.
export const SCENARIO_SECTIONS = [
  'connection',
  'style',
  'visualIdentity',
  'artifact',
  'pack',
  'assetSearch',
  'webhook',
  'admin',
] as const

export type ScenarioSection = (typeof SCENARIO_SECTIONS)[number]

// Human-readable panel labels for the post-import summary.
export const SECTION_LABELS: Record<ScenarioSection, string> = {
  connection: 'Connection',
  style: 'Styles',
  visualIdentity: 'Visual identity',
  artifact: 'Artifact generation',
  pack: 'Pack generation',
  assetSearch: 'Asset search',
  webhook: 'Webhook endpoint',
  admin: 'Admin job controls',
}

// Metadata keys allowed at the top level alongside the panel sections.
export const SCENARIO_META_KEYS = ['version', 'name'] as const
