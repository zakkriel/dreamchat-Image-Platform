import { useCallback, useEffect, useRef, useState } from 'react'
import { apiRequest, type ApiResult } from '../api/client'
import type { GenerationJob, JobAssetsResponse, VisualAsset } from '../api/types'
import { assetImageUrls } from '../util'
import { Button, Field, ImageGallery, JsonBlock, Panel, StatusBanner, TextInput } from './ui'

const TERMINAL = new Set(['completed', 'failed', 'cancelled'])

export function JobMonitorPanel() {
  const [jobId, setJobId] = useState('')
  const [job, setJob] = useState<GenerationJob | null>(null)
  const [jobResult, setJobResult] = useState<ApiResult<GenerationJob> | null>(null)
  const [timeline, setTimeline] = useState<string[]>([])
  const [polling, setPolling] = useState(false)
  const [assets, setAssets] = useState<VisualAsset[]>([])
  const [assetsResult, setAssetsResult] = useState<ApiResult<JobAssetsResponse> | null>(null)

  const lastStatus = useRef<string | null>(null)

  const fetchJob = useCallback(async () => {
    if (!jobId) return
    const res = await apiRequest<GenerationJob>({
      method: 'GET',
      path: `/v1/jobs/${encodeURIComponent(jobId)}`,
    })
    setJobResult(res)
    if (res.data) {
      setJob(res.data)
      if (res.data.status !== lastStatus.current) {
        lastStatus.current = res.data.status
        setTimeline((t) => [...t, `${new Date().toLocaleTimeString()} · ${res.data!.status}`])
      }
      if (TERMINAL.has(res.data.status)) setPolling(false)
    }
  }, [jobId])

  useEffect(() => {
    if (!polling) return
    void fetchJob()
    const id = setInterval(() => void fetchJob(), 2000)
    return () => clearInterval(id)
  }, [polling, fetchJob])

  async function fetchAssets() {
    if (!jobId) return
    const res = await apiRequest<JobAssetsResponse>({
      method: 'GET',
      path: `/v1/jobs/${encodeURIComponent(jobId)}/assets`,
    })
    setAssetsResult(res)
    if (res.data?.assets) setAssets(res.data.assets)
  }

  function resetTimeline() {
    setTimeline([])
    lastStatus.current = null
    setJob(null)
    setAssets([])
  }

  const galleryUrls = assets.flatMap((a) =>
    assetImageUrls(a).map((u) => ({ label: `${a.variant_key}:${u.label}`, url: u.url })),
  )

  return (
    <Panel title="5 · Job monitor" subtitle="Poll a job to completion, then fetch and preview its delivered assets.">
      <div className="grid">
        <Field label="job_id">
          <TextInput value={jobId} onChange={setJobId} placeholder="job_..." />
        </Field>
      </div>
      <div className="row">
        <Button onClick={() => void fetchJob()}>GET /v1/jobs/{'{id}'}</Button>
        <Button variant={polling ? 'danger' : 'primary'} onClick={() => setPolling((p) => !p)}>
          {polling ? 'Stop polling' : 'Poll (2s)'}
        </Button>
        <Button variant="secondary" onClick={() => void fetchAssets()}>
          GET /v1/jobs/{'{id}'}/assets
        </Button>
        <Button variant="secondary" onClick={resetTimeline}>
          Reset
        </Button>
      </div>

      {job && (
        <div className="job-summary">
          <span className={`pill pill-${job.status}`}>{job.status}</span>
          {job.error_code && <span className="pill pill-failed">{job.error_code}</span>}
          {job.error_message && <span className="muted">{job.error_message}</span>}
        </div>
      )}

      {timeline.length > 0 && (
        <div className="timeline">
          {timeline.map((t, i) => (
            <div key={i} className="timeline-row">
              {t}
            </div>
          ))}
        </div>
      )}

      <StatusBanner result={jobResult} />
      <ImageGallery urls={galleryUrls} />
      <StatusBanner result={assetsResult} />
      <JsonBlock label="job" value={job} />
      <JsonBlock label="assets" value={assetsResult?.data} />
    </Panel>
  )
}
