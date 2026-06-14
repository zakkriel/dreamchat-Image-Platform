// Connection config persisted to localStorage. An external store so the API
// client can read the current base URL / tokens synchronously, and React
// components can subscribe via useSyncExternalStore.
import { useSyncExternalStore } from 'react'

export interface PlaygroundConfig {
  baseUrl: string
  token: string
  adminToken: string
  activeStyleId: string
  // Last visual identity created/fetched in the Visual Identity panel, reused
  // by the Pack generation and Asset search panels.
  activeVisualIdentityId: string
  activeVisualIdentityOwnerType: '' | 'character' | 'place'
  activeVisualIdentityOwnerId: string
  activeVisualIdentityWorldId: string
}

const STORAGE_KEY = 'image-platform-playground.config'

// Default base URL is the Vite dev proxy prefix (`/api`), which forwards to the
// local API (http://localhost:8080 by default). Using the proxy avoids the
// backend's lack of CORS. Override to a full origin if your API serves CORS.
const DEFAULT_CONFIG: PlaygroundConfig = {
  baseUrl: '/api',
  token: '',
  adminToken: '',
  activeStyleId: '',
  activeVisualIdentityId: '',
  activeVisualIdentityOwnerType: '',
  activeVisualIdentityOwnerId: '',
  activeVisualIdentityWorldId: '',
}

function load(): PlaygroundConfig {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { ...DEFAULT_CONFIG }
    return { ...DEFAULT_CONFIG, ...(JSON.parse(raw) as Partial<PlaygroundConfig>) }
  } catch {
    return { ...DEFAULT_CONFIG }
  }
}

let config: PlaygroundConfig = load()
const listeners = new Set<() => void>()

export function getConfig(): PlaygroundConfig {
  return config
}

export function setConfig(patch: Partial<PlaygroundConfig>): void {
  config = { ...config, ...patch }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(config))
  } catch {
    // localStorage may be unavailable (private mode); keep in-memory copy.
  }
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function useConfig(): PlaygroundConfig {
  return useSyncExternalStore(subscribe, getConfig, getConfig)
}
