import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { setConfig, useConfig } from '../state/config'
import { Button, Field, JsonBlock, Panel, StatusBanner, TextInput } from './ui'

export function ConnectionPanel() {
  const cfg = useConfig()
  const [baseUrl, setBaseUrl] = useState(cfg.baseUrl)
  const [token, setToken] = useState(cfg.token)
  const [adminToken, setAdminToken] = useState(cfg.adminToken)
  const [health, setHealth] = useState<ApiResult | null>(null)
  const [openapiVersion, setOpenapiVersion] = useState<string | null>(null)
  const [openapiResult, setOpenapiResult] = useState<ApiResult | null>(null)

  function save() {
    setConfig({ baseUrl: baseUrl.trim(), token: token.trim(), adminToken: adminToken.trim() })
  }

  async function checkHealth() {
    save()
    setHealth(await apiRequest({ method: 'GET', path: '/health' }))
  }

  async function checkOpenapi() {
    save()
    const res = await apiRequest<{ info?: { version?: string } }>({
      method: 'GET',
      path: '/openapi.json',
    })
    setOpenapiResult(res)
    setOpenapiVersion(res.data?.info?.version ?? null)
  }

  return (
    <Panel
      title="1 · Connection"
      subtitle="Base URL + tokens are saved to localStorage. Default base URL `/api` is proxied to the local API by the Vite dev server."
    >
      <div className="grid">
        <Field label="API base URL">
          <TextInput value={baseUrl} onChange={setBaseUrl} placeholder="/api or http://localhost:8080" />
        </Field>
        <Field label="Bearer token (tenant)">
          <TextInput value={token} onChange={setToken} placeholder="dci_dev_..." />
        </Field>
        <Field label="Admin bearer token">
          <TextInput value={adminToken} onChange={setAdminToken} placeholder="dci_admin_..." />
        </Field>
      </div>

      <div className="row">
        <Button onClick={save}>Save to localStorage</Button>
        <Button variant="secondary" onClick={() => void checkHealth()}>
          GET /health
        </Button>
        <Button variant="secondary" onClick={() => void checkOpenapi()}>
          GET /openapi.json
        </Button>
        {openapiVersion && <span className="pill">OpenAPI v{openapiVersion}</span>}
      </div>

      <StatusBanner result={health} />
      <JsonBlock label="health" value={health?.raw} />
      <StatusBanner result={openapiResult} />
    </Panel>
  )
}
