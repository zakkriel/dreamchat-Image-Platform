// In-memory request log. Every API call the playground makes is recorded here
// (method, URL, status, duration, request/response JSON, and a copyable curl).
import { useSyncExternalStore } from 'react'

export interface LogEntry {
  id: string
  ts: string
  method: string
  url: string
  status: number | null
  durationMs: number
  requestBody?: unknown
  responseBody: unknown
  error?: string
  curl: string
}

const MAX_ENTRIES = 200
let entries: LogEntry[] = []
const listeners = new Set<() => void>()

function emit(): void {
  listeners.forEach((l) => l())
}

export function addEntry(entry: LogEntry): void {
  entries = [entry, ...entries].slice(0, MAX_ENTRIES)
  emit()
}

export function clearEntries(): void {
  entries = []
  emit()
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function getSnapshot(): LogEntry[] {
  return entries
}

export function useRequestLog(): LogEntry[] {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
