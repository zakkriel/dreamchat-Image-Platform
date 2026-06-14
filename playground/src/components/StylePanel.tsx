import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { setConfig, useConfig } from '../state/config'
import { QUALITY_TIERS, STYLE_MODES, type QualityTier, type StyleMode, type StyleProfile } from '../api/types'
import { Button, Field, JsonBlock, Panel, Select, StatusBanner, TextInput } from './ui'

export function StylePanel() {
  const cfg = useConfig()
  const [name, setName] = useState('Playground Style')
  const [styleMode, setStyleMode] = useState<StyleMode>('open_prompt')
  const [positivePrompt, setPositivePrompt] = useState('clean flat illustration, soft lighting')
  const [negativePrompt, setNegativePrompt] = useState('')
  const [quality, setQuality] = useState<QualityTier | ''>('standard')

  const [styles, setStyles] = useState<StyleProfile[]>([])
  const [createResult, setCreateResult] = useState<ApiResult | null>(null)
  const [listResult, setListResult] = useState<ApiResult | null>(null)

  async function listStyles() {
    const res = await apiRequest<{ styles: StyleProfile[] }>({ method: 'GET', path: '/v1/styles' })
    setListResult(res)
    if (res.data?.styles) setStyles(res.data.styles)
  }

  async function createStyle() {
    const body: Record<string, unknown> = {
      name,
      style_mode: styleMode,
      positive_prompt: positivePrompt,
    }
    if (negativePrompt) body.negative_prompt = negativePrompt
    if (quality) body.default_quality_tier = quality
    const res = await apiRequest<StyleProfile>({ method: 'POST', path: '/v1/styles', body })
    setCreateResult(res)
    if (res.data?.id) {
      setConfig({ activeStyleId: res.data.id })
      await listStyles()
    }
  }

  return (
    <Panel title="2 · Styles" subtitle="Create and list style profiles; pick the active style used by other panels.">
      <div className="grid">
        <Field label="name">
          <TextInput value={name} onChange={setName} />
        </Field>
        <Field label="style_mode">
          <Select value={styleMode} options={STYLE_MODES} onChange={(v) => v && setStyleMode(v)} />
        </Field>
        <Field label="default_quality_tier">
          <Select value={quality} options={QUALITY_TIERS} onChange={setQuality} allowEmpty />
        </Field>
        <Field label="positive_prompt">
          <TextInput value={positivePrompt} onChange={setPositivePrompt} />
        </Field>
        <Field label="negative_prompt">
          <TextInput value={negativePrompt} onChange={setNegativePrompt} />
        </Field>
      </div>

      <div className="row">
        <Button onClick={() => void createStyle()}>POST /v1/styles</Button>
        <Button variant="secondary" onClick={() => void listStyles()}>
          GET /v1/styles
        </Button>
      </div>

      <StatusBanner result={createResult} />
      <JsonBlock label="created style" value={createResult?.data} />

      <StatusBanner result={listResult} />
      {styles.length > 0 && (
        <Field label="Active style for generation tests">
          <select
            value={cfg.activeStyleId}
            onChange={(e) => setConfig({ activeStyleId: e.target.value })}
          >
            <option value="">(none selected)</option>
            {styles.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} · {s.id}
              </option>
            ))}
          </select>
        </Field>
      )}
      <JsonBlock label="styles" value={listResult?.data} />
    </Panel>
  )
}
