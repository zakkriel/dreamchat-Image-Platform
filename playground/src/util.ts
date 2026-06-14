// Small non-component helpers shared across panels.

export function pretty(value: unknown): string {
  if (value === null || value === undefined) return ''
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

export async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Clipboard API may be unavailable over non-secure origins; ignore.
  }
}

// Pull renderable image URLs out of a visual asset, preferring presigned
// download URLs (Phase 6B) over durable s3:// provenance fields.
export function assetImageUrls(asset: {
  thumbnail_download_url?: string
  preview_download_url?: string
  final_download_url?: string
  thumbnail_url?: string
  low_res_url?: string
  high_res_url?: string
}): { label: string; url: string }[] {
  const candidates: { label: string; url?: string }[] = [
    { label: 'thumbnail', url: asset.thumbnail_download_url },
    { label: 'preview', url: asset.preview_download_url },
    { label: 'final', url: asset.final_download_url },
  ]
  const out = candidates.filter(
    (c): c is { label: string; url: string } =>
      typeof c.url === 'string' && /^https?:\/\//.test(c.url),
  )
  return out
}
