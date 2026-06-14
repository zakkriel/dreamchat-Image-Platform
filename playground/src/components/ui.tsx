// Reusable presentational building blocks. Plain CSS classes, no design system.
import { type ReactNode } from 'react'
import { copyText, pretty } from '../util'

export function Panel({
  title,
  subtitle,
  children,
}: {
  title: string
  subtitle?: string
  children: ReactNode
}) {
  return (
    <section className="panel">
      <header className="panel-head">
        <h2>{title}</h2>
        {subtitle && <p className="muted">{subtitle}</p>}
      </header>
      <div className="panel-body">{children}</div>
    </section>
  )
}

export function Field({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
    </label>
  )
}

export function TextInput({
  value,
  onChange,
  placeholder,
  type = 'text',
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  type?: string
}) {
  return (
    <input
      type={type}
      value={value}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

export function Select<T extends string>({
  value,
  options,
  onChange,
  allowEmpty,
}: {
  value: T | ''
  options: readonly T[]
  onChange: (v: T | '') => void
  allowEmpty?: boolean
}) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value as T | '')}>
      {allowEmpty && <option value="">(unset)</option>}
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  )
}

export function Checkbox({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="checkbox">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  )
}

export function Button({
  onClick,
  children,
  disabled,
  variant = 'primary',
}: {
  onClick: () => void
  children: ReactNode
  disabled?: boolean
  variant?: 'primary' | 'secondary' | 'danger'
}) {
  return (
    <button className={`btn btn-${variant}`} onClick={onClick} disabled={disabled}>
      {children}
    </button>
  )
}

export function JsonBlock({ value, label }: { value: unknown; label?: string }) {
  if (value === null || value === undefined) return null
  const text = pretty(value)
  return (
    <div className="json-block">
      {label && <div className="json-label">{label}</div>}
      <pre>{text}</pre>
    </div>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  return (
    <button className="btn btn-secondary btn-sm" onClick={() => void copyText(text)}>
      {label}
    </button>
  )
}

export function StatusBanner({ result }: { result: { ok: boolean; status: number; error: string | null } | null }) {
  if (!result) return null
  if (result.ok) {
    return <div className="banner banner-ok">OK · HTTP {result.status}</div>
  }
  return (
    <div className="banner banner-err">
      Error · HTTP {result.status || 'network'} · {result.error}
    </div>
  )
}

export function ImageGallery({ urls }: { urls: { label: string; url: string }[] }) {
  if (urls.length === 0) return null
  return (
    <div className="gallery">
      {urls.map((u) => (
        <figure key={u.label + u.url} className="gallery-item">
          <a href={u.url} target="_blank" rel="noreferrer">
            <img src={u.url} alt={u.label} loading="lazy" />
          </a>
          <figcaption>{u.label}</figcaption>
        </figure>
      ))}
    </div>
  )
}
