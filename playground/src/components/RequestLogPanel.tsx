import { clearEntries, useRequestLog } from '../state/requestLog'
import { copyText, pretty } from '../util'
import { Button, Panel } from './ui'

export function RequestLogPanel() {
  const entries = useRequestLog()

  return (
    <Panel
      title="9 · Request log"
      subtitle="Every request the playground makes, newest first. Copy the curl equivalent to replay from a shell."
    >
      <div className="row">
        <Button variant="secondary" onClick={clearEntries} disabled={entries.length === 0}>
          Clear log
        </Button>
        <span className="muted">{entries.length} entr{entries.length === 1 ? 'y' : 'ies'}</span>
      </div>

      {entries.map((e) => (
        <details key={e.id} className="log-entry">
          <summary>
            <span className={`pill ${e.error ? 'pill-failed' : statusClass(e.status)}`}>
              {e.status ?? 'ERR'}
            </span>
            <span className="log-method">{e.method}</span>
            <span className="log-url">{e.url}</span>
            <span className="muted">{e.durationMs}ms</span>
          </summary>
          <div className="log-body">
            <div className="row">
              <button className="btn btn-secondary btn-sm" onClick={() => void copyText(e.curl)}>
                Copy curl
              </button>
              <code className="curl">{e.curl}</code>
            </div>
            {e.error && <div className="banner banner-err">{e.error}</div>}
            {e.requestBody !== undefined && (
              <div className="json-block">
                <div className="json-label">request body</div>
                <pre>{pretty(e.requestBody)}</pre>
              </div>
            )}
            <div className="json-block">
              <div className="json-label">response</div>
              <pre>{pretty(e.responseBody)}</pre>
            </div>
          </div>
        </details>
      ))}
    </Panel>
  )
}

function statusClass(status: number | null): string {
  if (status === null) return 'pill-failed'
  if (status >= 200 && status < 300) return 'pill-completed'
  if (status >= 400) return 'pill-failed'
  return ''
}
