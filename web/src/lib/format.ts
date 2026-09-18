import { format } from 'date-fns'

export function fmtTokens(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(2) + 'K'
  return Math.round(n).toLocaleString()
}

export function fmtCost(n: number): string {
  if (n < 0) return '-' + fmtCost(-n)
  return '$' + n.toFixed(2)
}

// pctChange is the percent change from prev to cur, or null when there's no
// prior baseline to compare against (so the UI can hide the delta).
export function pctChange(prev: number, cur: number): number | null {
  if (prev === 0) return cur === 0 ? 0 : null
  return ((cur - prev) / prev) * 100
}

export function fmtDuration(seconds: number): string {
  if (seconds >= 1) return seconds.toFixed(2) + 's'
  if (seconds >= 0.001) return (seconds * 1000).toFixed(0) + 'ms'
  return (seconds * 1_000_000).toFixed(0) + 'µs'
}

// fmtBucket renders a time-axis label appropriate for the visible span. The
// sub-daily cutoff matches resolveRange's 6-hour bucketing band (up to 14d) so
// multiple buckets on one day don't collapse to the same day-only label; only
// the >14d band (1-day buckets) drops the time component.
export function fmtBucket(bucket: string, spanMs: number): string {
  const d = new Date(bucket)
  if (spanMs <= 26 * 3600e3) return format(d, 'HH:mm')
  if (spanMs <= 14 * 24 * 3600e3) return format(d, 'EEE HH:mm')
  return format(d, 'MMM d')
}

export function fmtTime(bucket: string): string {
  return format(new Date(bucket), 'MMM d, HH:mm')
}

// SPECIAL_CASING maps a lowercased segment to its preferred display casing,
// covering acronyms and brand names that plain title-casing would mangle.
const SPECIAL_CASING: Record<string, string> = {
  gpt: 'GPT',
  oss: 'OSS',
  ai: 'AI',
  xai: 'xAI',
  qwen: 'Qwen',
  qwq: 'QwQ',
  glm: 'GLM',
  llm: 'LLM',
  moe: 'MoE',
  tts: 'TTS',
  nemotron: 'Nemotron',
  deepseek: 'DeepSeek',
  minimax: 'MiniMax',
  openai: 'OpenAI',
  codex: 'Codex',
  phi: 'Phi',
  kimi: 'Kimi',
  grok: 'Grok',
  llama: 'Llama',
  gemma: 'Gemma',
  gemini: 'Gemini',
  mistral: 'Mistral',
  codestral: 'Codestral',
  devstral: 'Devstral',
  ministral: 'Ministral',
  magistral: 'Magistral',
  pixtral: 'Pixtral',
  voxtral: 'Voxtral',
  claude: 'Claude',
  sonnet: 'Sonnet',
  opus: 'Opus',
  haiku: 'Haiku',
}

// fmtSegment applies known casing, then formats a raw id segment: brand/acronym
// lookups win; otherwise size suffixes (70b → 70B) and letter-led tokens are
// cased by their leading letter-run (r1 → R1, qwen3 → Qwen3), while OpenAI's
// lowercase o-series (o1, o3-mini) and mixed version tokens (4o, 3.5) are kept.
function fmtSegment(seg: string): string {
  const lower = seg.toLowerCase()
  if (SPECIAL_CASING[lower]) return SPECIAL_CASING[lower]
  if (!/\d/.test(seg)) return lower.charAt(0).toUpperCase() + lower.slice(1)
  if (/^\d+b$/.test(lower)) return lower.toUpperCase() // 70b → 70B
  if (/^o\d/.test(lower)) return lower // OpenAI o-series: o1, o3
  // Letter-run then digits (r1, k2.7, qwen3): case the letters (known brand or
  // title-case), keep the numeric tail verbatim.
  const m = /^([a-z]+)(\d.*)$/.exec(lower)
  if (m) {
    const head = SPECIAL_CASING[m[1]] ?? m[1].charAt(0).toUpperCase() + m[1].slice(1)
    return head + m[2]
  }
  return lower // 4o, 3.5, v2
}

// fmtModelName turns a raw model id into a readable display name — e.g.
// "gpt-4o" → "GPT 4o", "claude-sonnet-4-20250514" → "Claude Sonnet 4". It drops
// vendor path prefixes (openai/…) and any @date / @default / :suffix qualifier
// and trailing date or build stamp, collapses split version numbers
// (sonnet-4-5 → Sonnet 4.5), then applies acronym/brand casing per segment.
export function fmtModelName(model: string): string {
  if (!model) return model
  let cleaned = model.replace(/[@:].*$/, '') // @20250805 / @default / :thinking qualifier
  // Drop vendor/org path prefix, keeping only the final model segment.
  cleaned = cleaned.slice(cleaned.lastIndexOf('/') + 1)
  cleaned = cleaned
    .replace(/[-_]?\d{4}-\d{2}-\d{2}$/, '') // trailing YYYY-MM-DD
    .replace(/[-_]?\d{8}$/, '') // trailing YYYYMMDD
    .replace(/[-_]?\d{6,}$/, '') // trailing numeric build id
    // Collapse a hyphen-split version like "4-5" or "4-1" into "4.5"/"4.1",
    // the form vendors (Anthropic, xAI) print — but only between bare integers.
    .replace(/(\b\d+)-(\d+\b)/g, '$1.$2')
  const parts = cleaned.split(/[-_\s]+/).filter(Boolean)
  if (parts.length === 0) return model
  return parts.map(fmtSegment).join(' ')
}
