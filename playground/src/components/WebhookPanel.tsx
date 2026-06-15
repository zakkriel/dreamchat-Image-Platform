import { useEffect, useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import { getScenario, useScenarioSeq } from '../scenario/store'
import type { WebhookEndpoint, WebhookEndpointWithSecret } from '../api/types'
import { Button, CopyButton, Field, JsonBlock, Panel, StatusBanner, TextInput } from './ui'

export function WebhookPanel() {
  const [url, setUrl] = useState('https://webhook.site/your-id')
  const [putResult, setPutResult] = useState<ApiResult<WebhookEndpointWithSecret> | null>(null)
  const [getResult, setGetResult] = useState<ApiResult<WebhookEndpoint> | null>(null)

  const seq = useScenarioSeq()
  useEffect(() => {
    if (seq === 0) return
    const s = getScenario()?.webhook
    if (!s) return
    if (s.url !== undefined) setUrl(s.url)
  }, [seq])

  async function putEndpoint() {
    setPutResult(
      await apiRequest<WebhookEndpointWithSecret>({
        method: 'PUT',
        path: '/v1/admin/webhook-endpoint',
        body: { url },
        admin: true,
      }),
    )
  }

  async function getEndpoint() {
    setGetResult(
      await apiRequest<WebhookEndpoint>({
        method: 'GET',
        path: '/v1/admin/webhook-endpoint',
        admin: true,
      }),
    )
  }

  const secret = putResult?.data?.secret

  return (
    <Panel
      title="8 · Webhook endpoint"
      subtitle="Admin token required. Configure the tenant's one signed webhook endpoint."
    >
      <p className="muted note">
        Use <code>webhook.site</code> or a local tunnel (e.g. <code>ngrok</code>) as the URL to inspect
        delivered job-lifecycle events. The signing secret is shown once, only on PUT. Replay / DLQ /
        rotation are intentionally not part of this playground.
      </p>
      <div className="grid">
        <Field label="url">
          <TextInput value={url} onChange={setUrl} placeholder="https://webhook.site/..." />
        </Field>
      </div>
      <div className="row">
        <Button onClick={() => void putEndpoint()}>PUT /v1/admin/webhook-endpoint</Button>
        <Button variant="secondary" onClick={() => void getEndpoint()}>
          GET /v1/admin/webhook-endpoint
        </Button>
      </div>

      <StatusBanner result={putResult} />
      {secret && (
        <div className="banner banner-ok">
          <strong>signing secret (shown once):</strong> <code>{secret}</code> <CopyButton text={secret} />
        </div>
      )}
      <JsonBlock label="PUT response" value={putResult?.data} />

      <StatusBanner result={getResult} />
      <JsonBlock label="GET response" value={getResult?.data} />
    </Panel>
  )
}
