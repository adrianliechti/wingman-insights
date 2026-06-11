import { useNavigate } from '@tanstack/react-router'
import { Bot, Boxes, Cpu, FilterX, User } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useApi, useDash } from '../dash'
import type { DashSearch } from '../dash'
import type { FilterOptions } from '../types'

function FilterSelect({
  icon: Icon,
  placeholder,
  value,
  options,
  onChange,
}: {
  icon: LucideIcon
  placeholder: string
  value: string | undefined
  options: { value: string; label: string }[]
  onChange: (v: string | undefined) => void
}) {
  return (
    <label className="flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 dark:border-gray-800 dark:bg-gray-900">
      <Icon className={`h-3.5 w-3.5 ${value ? 'text-indigo-500' : 'text-gray-400 dark:text-gray-600'}`} />
      <select
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value || undefined)}
        className={`bg-transparent text-xs font-medium outline-none ${
          value ? 'text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'
        }`}
      >
        <option value="">{placeholder}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  )
}

export function FilterBar() {
  const { search } = useDash()
  const navigate = useNavigate()
  // The options list itself must not shrink to the current selection, so this
  // request carries only the time range, not the active filters.
  const { data } = useApi<FilterOptions>('/api/filters', {
    service: undefined,
    user: undefined,
    provider: undefined,
    model: undefined,
    interval: undefined,
  })

  function setFilter(patch: Partial<DashSearch>) {
    navigate({ to: '.', search: (prev: DashSearch) => ({ ...prev, ...patch }) })
  }

  const hasFilter = !!(search.service || search.user || search.provider || search.model)

  return (
    <div className="flex flex-wrap items-center gap-2 py-4">
      <FilterSelect
        icon={Boxes}
        placeholder="All services"
        value={search.service}
        options={(data?.services ?? []).map((s) => ({ value: s, label: s }))}
        onChange={(v) => setFilter({ service: v })}
      />
      <FilterSelect
        icon={User}
        placeholder="All users"
        value={search.user}
        options={(data?.users ?? []).map((u) => ({ value: u.id, label: u.email || u.id }))}
        onChange={(v) => setFilter({ user: v })}
      />
      <FilterSelect
        icon={Bot}
        placeholder="All providers"
        value={search.provider}
        options={(data?.providers ?? []).map((p) => ({ value: p, label: p }))}
        onChange={(v) => setFilter({ provider: v })}
      />
      <FilterSelect
        icon={Cpu}
        placeholder="All models"
        value={search.model}
        options={(data?.models ?? []).map((m) => ({ value: m, label: m }))}
        onChange={(v) => setFilter({ model: v })}
      />
      {hasFilter && (
        <button
          onClick={() => setFilter({ service: undefined, user: undefined, provider: undefined, model: undefined })}
          className="flex items-center gap-1 rounded-lg px-2 py-1.5 text-xs font-medium text-gray-500 hover:text-gray-900 dark:hover:text-white"
        >
          <FilterX className="h-3.5 w-3.5" />
          Clear
        </button>
      )}
    </div>
  )
}
