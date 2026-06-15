// Lightweight shared store for the most recently imported scenario.
//
// Panel form state stays local to each component (existing behavior). When a
// scenario is imported, panels read the relevant section from here and copy it
// into their own state — once, keyed off a monotonically increasing sequence
// number — so manual editing afterwards is preserved. This mirrors the external
// store pattern already used by `state/config.ts`.
import { useSyncExternalStore } from 'react'
import type { Scenario } from './types'

let current: Scenario | null = null
let seq = 0
const listeners = new Set<() => void>()

// Imperative getter so panel effects can read the latest scenario without
// listing it as a dependency (the effect depends only on `seq`).
export function getScenario(): Scenario | null {
  return current
}

export function applyScenario(scenario: Scenario): void {
  current = scenario
  seq += 1
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function getSeq(): number {
  return seq
}

// Returns the current import sequence number. Starts at 0 and increments on
// every import; panels re-apply their section whenever it changes.
export function useScenarioSeq(): number {
  return useSyncExternalStore(subscribe, getSeq, getSeq)
}
