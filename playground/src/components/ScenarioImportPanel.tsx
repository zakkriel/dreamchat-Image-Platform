import { useRef, useState } from 'react'
import { importScenario } from '../scenario/importScenario'
import { applyScenario } from '../scenario/store'
import { SECTION_LABELS, type ScenarioSection } from '../scenario/types'
import { Button, Field, Panel, TextArea } from './ui'

export function ScenarioImportPanel() {
  const [text, setText] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [filled, setFilled] = useState<ScenarioSection[] | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  function runImport(source: string) {
    const result = importScenario(source)
    if (!result.ok) {
      setErrors(result.errors)
      setFilled(null)
      return
    }
    applyScenario(result.scenario)
    setErrors([])
    setFilled(result.sections)
  }

  function onFile(file: File) {
    const reader = new FileReader()
    reader.onload = () => {
      const content = typeof reader.result === 'string' ? reader.result : ''
      setText(content)
      runImport(content)
    }
    reader.onerror = () => {
      setErrors([`Could not read file: ${file.name}`])
      setFilled(null)
    }
    reader.readAsText(file)
  }

  return (
    <Panel
      title="0 · Scenario import"
      subtitle="Dev-only. Load a JSON scenario to pre-fill the panels below. Nothing is uploaded or auto-submitted — import only fills form fields; you still press each panel's button to call the API."
    >
      <div className="row">
        <input
          ref={fileInput}
          type="file"
          accept="application/json,.json"
          style={{ display: 'none' }}
          onChange={(e) => {
            const file = e.target.files?.[0]
            if (file) onFile(file)
            // Reset so selecting the same file again re-fires onChange.
            e.target.value = ''
          }}
        />
        <Button variant="secondary" onClick={() => fileInput.current?.click()}>
          Choose .json file…
        </Button>
        <span className="muted">or paste JSON below</span>
      </div>

      <Field label="scenario JSON">
        <TextArea
          value={text}
          onChange={setText}
          rows={10}
          placeholder='{ "connection": { "baseUrl": "/api" }, "style": { ... } }'
        />
      </Field>

      <div className="row">
        <Button onClick={() => runImport(text)} disabled={!text.trim()}>
          Import scenario
        </Button>
        <Button
          variant="secondary"
          onClick={() => {
            setText('')
            setErrors([])
            setFilled(null)
          }}
          disabled={!text && errors.length === 0 && !filled}
        >
          Clear
        </Button>
      </div>

      {errors.length > 0 && (
        <div className="banner banner-err">
          <strong>Import failed:</strong>
          <ul className="scenario-errors">
            {errors.map((e, i) => (
              <li key={i}>{e}</li>
            ))}
          </ul>
        </div>
      )}

      {filled && (
        <div className="banner banner-ok">
          {filled.length === 0 ? (
            <span>Imported, but no panel sections were present.</span>
          ) : (
            <>
              <strong>Imported.</strong> Filled panels:{' '}
              {filled.map((s) => (
                <span key={s} className="pill">
                  {SECTION_LABELS[s]}
                </span>
              ))}
            </>
          )}
          <p className="muted note">
            Token fields and any sections not present were left untouched. Edit any field before
            calling the API.
          </p>
        </div>
      )}
    </Panel>
  )
}
