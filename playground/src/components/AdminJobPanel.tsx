import { useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import type { GenerationJob } from '../api/types'
import { Button, Field, JsonBlock, Panel, StatusBanner, TextInput } from './ui'

export function AdminJobPanel() {
  const [jobId, setJobId] = useState('')
  const [retryResult, setRetryResult] = useState<ApiResult<GenerationJob> | null>(null)
  const [cancelResult, setCancelResult] = useState<ApiResult<GenerationJob> | null>(null)

  async function retry() {
    setRetryResult(
      await apiRequest<GenerationJob>({
        method: 'POST',
        path: `/v1/admin/jobs/${encodeURIComponent(jobId)}/retry`,
        admin: true,
      }),
    )
  }

  async function cancel() {
    setCancelResult(
      await apiRequest<GenerationJob>({
        method: 'POST',
        path: `/v1/admin/jobs/${encodeURIComponent(jobId)}/cancel`,
        admin: true,
      }),
    )
  }

  return (
    <Panel
      title="8 · Admin job controls"
      subtitle="Admin token required. Retry a failed job or cancel a queued/running/preview_ready job."
    >
      <div className="grid">
        <Field label="job_id">
          <TextInput value={jobId} onChange={setJobId} placeholder="job_..." />
        </Field>
      </div>
      <div className="row">
        <Button onClick={() => void retry()}>POST .../retry</Button>
        <Button variant="danger" onClick={() => void cancel()}>
          POST .../cancel
        </Button>
      </div>

      <StatusBanner result={retryResult} />
      <JsonBlock label="retry response" value={retryResult?.raw} />
      <StatusBanner result={cancelResult} />
      <JsonBlock label="cancel response" value={cancelResult?.raw} />
    </Panel>
  )
}
