// Parsing + validation for imported playground scenarios.
//
// Validation runs before anything is applied to the panels: malformed JSON,
// unknown top-level sections, and badly typed fields all produce clear errors
// and abort the import. Nothing here performs network I/O or persists the
// scenario anywhere — it only turns untrusted text into a validated `Scenario`.
import {
  DELIVERY_MODES,
  FALLBACK_POLICIES,
  LATENCY_TIERS,
  QUALITY_TIERS,
  STYLE_MODES,
} from '../api/types'
import {
  SCENARIO_META_KEYS,
  SCENARIO_SECTIONS,
  type Scenario,
  type ScenarioSection,
} from './types'

export interface ImportSuccess {
  ok: true
  scenario: Scenario
  // Sections that were present and will fill a panel, in panel order.
  sections: ScenarioSection[]
}

export interface ImportFailure {
  ok: false
  errors: string[]
}

export type ImportResult = ImportSuccess | ImportFailure

const PACK_ENTITIES = ['character', 'place'] as const

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

// Accumulates validation errors for one section, namespaced by a path prefix.
class Checker {
  readonly errors: string[] = []
  constructor(private readonly path: string) {}

  private at(field: string): string {
    return `${this.path}.${field}`
  }

  str(obj: Record<string, unknown>, field: string): string | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (typeof v !== 'string') {
      this.errors.push(`${this.at(field)} must be a string`)
      return undefined
    }
    return v
  }

  bool(obj: Record<string, unknown>, field: string): boolean | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (typeof v !== 'boolean') {
      this.errors.push(`${this.at(field)} must be a boolean`)
      return undefined
    }
    return v
  }

  int(obj: Record<string, unknown>, field: string): number | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (typeof v !== 'number' || !Number.isInteger(v)) {
      this.errors.push(`${this.at(field)} must be an integer`)
      return undefined
    }
    return v
  }

  obj(obj: Record<string, unknown>, field: string): Record<string, unknown> | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (!isPlainObject(v)) {
      this.errors.push(`${this.at(field)} must be an object`)
      return undefined
    }
    return v
  }

  // A string field constrained to a fixed set of values. `''` is always allowed
  // (it maps to a panel's "unset" option).
  enum<T extends string>(
    obj: Record<string, unknown>,
    field: string,
    allowed: readonly T[],
  ): T | '' | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (typeof v !== 'string') {
      this.errors.push(`${this.at(field)} must be a string`)
      return undefined
    }
    if (v === '') return ''
    if (!(allowed as readonly string[]).includes(v)) {
      this.errors.push(`${this.at(field)} must be one of: ${allowed.join(', ')}`)
      return undefined
    }
    return v as T
  }

  // Like `enum`, but empty string is not a valid value (the panel control has
  // no "unset" option).
  enumStrict<T extends string>(
    obj: Record<string, unknown>,
    field: string,
    allowed: readonly T[],
  ): T | undefined {
    const v = obj[field]
    if (v === undefined) return undefined
    if (typeof v !== 'string' || !(allowed as readonly string[]).includes(v)) {
      this.errors.push(`${this.at(field)} must be one of: ${allowed.join(', ')}`)
      return undefined
    }
    return v as T
  }

  // Reject keys that aren't part of the section's schema.
  unknownKeys(obj: Record<string, unknown>, allowed: readonly string[]): void {
    for (const key of Object.keys(obj)) {
      if (!allowed.includes(key)) this.errors.push(`${this.at(key)} is not a known field`)
    }
  }
}

function copyDefined<T extends object>(target: T, entries: Partial<T>): void {
  for (const [k, v] of Object.entries(entries)) {
    if (v !== undefined) (target as Record<string, unknown>)[k] = v
  }
}

/**
 * Parse and validate raw scenario text. On success returns the typed scenario
 * plus the list of sections that will fill a panel; on failure returns the
 * collected validation errors.
 */
export function importScenario(text: string): ImportResult {
  const trimmed = text.trim()
  if (!trimmed) return { ok: false, errors: ['No scenario provided — paste JSON or choose a .json file.'] }

  let raw: unknown
  try {
    raw = JSON.parse(trimmed)
  } catch (e) {
    return { ok: false, errors: [`Invalid JSON: ${e instanceof Error ? e.message : String(e)}`] }
  }

  if (!isPlainObject(raw)) {
    return { ok: false, errors: ['Scenario must be a JSON object at the top level.'] }
  }

  const errors: string[] = []

  // Reject unknown top-level keys (anything that is neither metadata nor a
  // known panel section).
  const allowedTop = [...SCENARIO_META_KEYS, ...SCENARIO_SECTIONS] as readonly string[]
  for (const key of Object.keys(raw)) {
    if (!allowedTop.includes(key)) errors.push(`Unknown section "${key}".`)
  }

  if (raw.version !== undefined && (typeof raw.version !== 'number' || !Number.isInteger(raw.version))) {
    errors.push('version must be an integer')
  }
  if (raw.name !== undefined && typeof raw.name !== 'string') {
    errors.push('name must be a string')
  }

  const scenario: Scenario = {}
  if (typeof raw.version === 'number') scenario.version = raw.version
  if (typeof raw.name === 'string') scenario.name = raw.name

  // Each section validates independently; an absent section is simply skipped.
  if (raw.connection !== undefined) {
    if (!isPlainObject(raw.connection)) {
      errors.push('connection must be an object')
    } else {
      const c = new Checker('connection')
      c.unknownKeys(raw.connection, ['baseUrl', 'token', 'adminToken'])
      const section = {
        baseUrl: c.str(raw.connection, 'baseUrl'),
        token: c.str(raw.connection, 'token'),
        adminToken: c.str(raw.connection, 'adminToken'),
      }
      errors.push(...c.errors)
      scenario.connection = {}
      copyDefined(scenario.connection, section)
    }
  }

  if (raw.style !== undefined) {
    if (!isPlainObject(raw.style)) {
      errors.push('style must be an object')
    } else {
      const c = new Checker('style')
      c.unknownKeys(raw.style, [
        'name',
        'styleMode',
        'positivePrompt',
        'negativePrompt',
        'defaultQualityTier',
      ])
      const section = {
        name: c.str(raw.style, 'name'),
        styleMode: c.enumStrict(raw.style, 'styleMode', STYLE_MODES),
        positivePrompt: c.str(raw.style, 'positivePrompt'),
        negativePrompt: c.str(raw.style, 'negativePrompt'),
        defaultQualityTier: c.enum(raw.style, 'defaultQualityTier', QUALITY_TIERS),
      }
      errors.push(...c.errors)
      scenario.style = {}
      copyDefined(scenario.style, section)
    }
  }

  if (raw.visualIdentity !== undefined) {
    if (!isPlainObject(raw.visualIdentity)) {
      errors.push('visualIdentity must be an object')
    } else {
      const c = new Checker('visualIdentity')
      c.unknownKeys(raw.visualIdentity, [
        'ownerType',
        'worldId',
        'ownerId',
        'displayName',
        'canonicalVisualTraits',
        'styleProfileId',
        'consistencyKey',
      ])
      const section = {
        ownerType: c.enumStrict(raw.visualIdentity, 'ownerType', PACK_ENTITIES),
        worldId: c.str(raw.visualIdentity, 'worldId'),
        ownerId: c.str(raw.visualIdentity, 'ownerId'),
        displayName: c.str(raw.visualIdentity, 'displayName'),
        canonicalVisualTraits: c.obj(raw.visualIdentity, 'canonicalVisualTraits'),
        styleProfileId: c.str(raw.visualIdentity, 'styleProfileId'),
        consistencyKey: c.str(raw.visualIdentity, 'consistencyKey'),
      }
      errors.push(...c.errors)
      scenario.visualIdentity = {}
      copyDefined(scenario.visualIdentity, section)
    }
  }

  if (raw.artifact !== undefined) {
    if (!isPlainObject(raw.artifact)) {
      errors.push('artifact must be an object')
    } else {
      const c = new Checker('artifact')
      c.unknownKeys(raw.artifact, [
        'artifactId',
        'worldId',
        'styleProfileId',
        'description',
        'qualityTier',
        'latencyTier',
        'deliveryMode',
        'providerId',
        'forceRegenerate',
        'idempotencyKey',
      ])
      const section = {
        artifactId: c.str(raw.artifact, 'artifactId'),
        worldId: c.str(raw.artifact, 'worldId'),
        styleProfileId: c.str(raw.artifact, 'styleProfileId'),
        description: c.str(raw.artifact, 'description'),
        qualityTier: c.enum(raw.artifact, 'qualityTier', QUALITY_TIERS),
        latencyTier: c.enum(raw.artifact, 'latencyTier', LATENCY_TIERS),
        deliveryMode: c.enum(raw.artifact, 'deliveryMode', DELIVERY_MODES),
        providerId: c.str(raw.artifact, 'providerId'),
        forceRegenerate: c.bool(raw.artifact, 'forceRegenerate'),
        idempotencyKey: c.str(raw.artifact, 'idempotencyKey'),
      }
      errors.push(...c.errors)
      scenario.artifact = {}
      copyDefined(scenario.artifact, section)
    }
  }

  if (raw.pack !== undefined) {
    if (!isPlainObject(raw.pack)) {
      errors.push('pack must be an object')
    } else {
      const c = new Checker('pack')
      c.unknownKeys(raw.pack, ['character', 'place'])
      scenario.pack = {}
      for (const entity of PACK_ENTITIES) {
        const sub = raw.pack[entity]
        if (sub === undefined) continue
        if (!isPlainObject(sub)) {
          errors.push(`pack.${entity} must be an object`)
          continue
        }
        const ec = new Checker(`pack.${entity}`)
        ec.unknownKeys(sub, [
          'entityId',
          'worldId',
          'styleProfileId',
          'packTemplate',
          'qualityTier',
          'providerId',
          'forceRegenerate',
        ])
        const section = {
          entityId: ec.str(sub, 'entityId'),
          worldId: ec.str(sub, 'worldId'),
          styleProfileId: ec.str(sub, 'styleProfileId'),
          packTemplate: ec.str(sub, 'packTemplate'),
          qualityTier: ec.enum(sub, 'qualityTier', QUALITY_TIERS),
          providerId: ec.str(sub, 'providerId'),
          forceRegenerate: ec.bool(sub, 'forceRegenerate'),
        }
        errors.push(...ec.errors)
        scenario.pack[entity] = {}
        copyDefined(scenario.pack[entity]!, section)
      }
      errors.push(...c.errors)
    }
  }

  if (raw.assetSearch !== undefined) {
    if (!isPlainObject(raw.assetSearch)) {
      errors.push('assetSearch must be an object')
    } else {
      const c = new Checker('assetSearch')
      c.unknownKeys(raw.assetSearch, [
        'worldId',
        'ownerType',
        'visualIdentityId',
        'variantKey',
        'styleProfileId',
        'stateVersion',
        'qualityTier',
        'fallbackPolicy',
      ])
      const section = {
        worldId: c.str(raw.assetSearch, 'worldId'),
        ownerType: c.enumStrict(raw.assetSearch, 'ownerType', PACK_ENTITIES),
        visualIdentityId: c.str(raw.assetSearch, 'visualIdentityId'),
        variantKey: c.str(raw.assetSearch, 'variantKey'),
        styleProfileId: c.str(raw.assetSearch, 'styleProfileId'),
        stateVersion: c.int(raw.assetSearch, 'stateVersion'),
        qualityTier: c.enum(raw.assetSearch, 'qualityTier', QUALITY_TIERS),
        fallbackPolicy: c.enum(raw.assetSearch, 'fallbackPolicy', FALLBACK_POLICIES),
      }
      errors.push(...c.errors)
      scenario.assetSearch = {}
      copyDefined(scenario.assetSearch, section)
    }
  }

  if (raw.webhook !== undefined) {
    if (!isPlainObject(raw.webhook)) {
      errors.push('webhook must be an object')
    } else {
      const c = new Checker('webhook')
      c.unknownKeys(raw.webhook, ['url'])
      const section = { url: c.str(raw.webhook, 'url') }
      errors.push(...c.errors)
      scenario.webhook = {}
      copyDefined(scenario.webhook, section)
    }
  }

  if (raw.admin !== undefined) {
    if (!isPlainObject(raw.admin)) {
      errors.push('admin must be an object')
    } else {
      const c = new Checker('admin')
      c.unknownKeys(raw.admin, ['jobId'])
      const section = { jobId: c.str(raw.admin, 'jobId') }
      errors.push(...c.errors)
      scenario.admin = {}
      copyDefined(scenario.admin, section)
    }
  }

  if (errors.length > 0) return { ok: false, errors }

  const sections = SCENARIO_SECTIONS.filter((s) => scenario[s] !== undefined)
  return { ok: true, scenario, sections }
}
