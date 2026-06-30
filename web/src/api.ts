export type Params = Record<string, string | string[] | undefined>

export function apiUrl(path: string, params: Params): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (Array.isArray(value)) {
      const items = value.filter(Boolean)
      if (items.length > 0) search.set(key, items.join(','))
    } else if (value) {
      search.set(key, value)
    }
  }
  // Drop the leading slash so the URL is relative and resolves against the
  // document's <base href>: the /api endpoints live under the same UI base
  // path (e.g. /insights/api/...). Only OTLP /v1 ingest stays at the root.
  return `${path.replace(/^\//, '')}?${search.toString()}`
}

export async function apiGet<T>(path: string, params: Params, signal?: AbortSignal): Promise<T> {
  const res = await fetch(apiUrl(path, params), { signal })
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
  return res.json()
}
