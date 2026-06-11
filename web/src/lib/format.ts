import { format } from 'date-fns'

export function fmtTokens(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(1) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return n.toFixed(0)
}

export function fmtCost(n: number): string {
  if (n >= 100) return '$' + n.toFixed(0)
  if (n >= 1) return '$' + n.toFixed(2)
  if (n > 0) return '$' + n.toFixed(4)
  return '$0'
}

export function fmtDuration(seconds: number): string {
  if (seconds >= 1) return seconds.toFixed(2) + 's'
  if (seconds >= 0.001) return (seconds * 1000).toFixed(0) + 'ms'
  return (seconds * 1_000_000).toFixed(0) + 'µs'
}

// fmtBucket renders a time-axis label appropriate for the visible span.
export function fmtBucket(bucket: string, spanMs: number): string {
  const d = new Date(bucket)
  if (spanMs <= 26 * 3600e3) return format(d, 'HH:mm')
  if (spanMs <= 4 * 24 * 3600e3) return format(d, 'EEE HH:mm')
  return format(d, 'MMM d')
}

export function fmtTime(bucket: string): string {
  return format(new Date(bucket), 'MMM d, HH:mm')
}
