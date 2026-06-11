export type Params = Record<string, string | undefined>

export function apiUrl(path: string, params: Params): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value)
  }
  return `${path}?${search.toString()}`
}

export async function apiGet<T>(path: string, params: Params): Promise<T> {
  const res = await fetch(apiUrl(path, params))
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
  return res.json()
}
