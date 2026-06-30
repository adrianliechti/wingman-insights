// Shared rendering for resolved principals across the dashboard tables. The
// backend adds `name` (Entra display name) and `kind` (user | application) to
// the raw OTel `id`; we show the name when known and keep the raw id as
// muted subtext. Email is intentionally not shown.

export interface UserLike {
  id?: string
  name?: string
  kind?: string
}

// userLabel is the primary display string: the resolved name, else the raw id.
export function userLabel(r: UserLike): string {
  return r.name || r.id || 'unattributed'
}

// UserCell shows the name (or raw id) with the raw id as subtext when resolved.
export function UserCell({ r }: { r: UserLike }) {
  return (
    <div className="flex flex-col leading-tight">
      <span className="text-xs text-gray-900 dark:text-gray-100">{userLabel(r)}</span>
      {r.name && r.id && (
        <span className="font-mono text-[10px] text-gray-400 dark:text-gray-600">{r.id}</span>
      )}
    </div>
  )
}

// AppCell renders a resolved application the same way UserCell renders a user:
// display name with the raw app id as subtext, else the bare id.
export function AppCell({ id, name }: { id?: string; name?: string }) {
  return <UserCell r={{ id, name }} />
}

const KIND_STYLE: Record<string, string> = {
  user: 'bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300',
  application: 'bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-300',
}

// KindBadge tags a principal as a user or an application; renders a dash when
// the directory hasn't resolved it.
export function KindBadge({ kind }: { kind?: string }) {
  if (!kind) return <span className="text-gray-400 dark:text-gray-600">—</span>
  const label = kind === 'application' ? 'App' : kind === 'user' ? 'User' : kind
  return (
    <span className={`rounded px-1.5 py-0.5 text-[11px] font-medium ${KIND_STYLE[kind] ?? 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300'}`}>
      {label}
    </span>
  )
}
