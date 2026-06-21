import { useState } from 'react'
import {
  Combobox,
  ComboboxButton,
  ComboboxInput,
  ComboboxOption,
  ComboboxOptions,
} from '@headlessui/react'
import { useNavigate } from '@tanstack/react-router'
import { Bot, Boxes, Check, ChevronDown, Cpu, FilterX, User } from 'lucide-react'
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
  const [query, setQuery] = useState('')
  const selected = options.find((o) => o.value === value)
  const filtered =
    query === '' ? options : options.filter((o) => o.label.toLowerCase().includes(query.toLowerCase()))

  return (
    <Combobox
      value={value ?? ''}
      onChange={(v: string | null) => {
        onChange(v || undefined)
        setQuery('')
      }}
    >
      <div className="relative">
        <div
          className={`flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors ${
            value
              ? 'border-indigo-300 bg-indigo-50 dark:border-indigo-500/40 dark:bg-indigo-500/10'
              : 'border-gray-200 bg-white hover:border-gray-300 dark:border-gray-800 dark:bg-gray-900 dark:hover:border-gray-700'
          }`}
        >
          <Icon className={`h-3.5 w-3.5 shrink-0 ${value ? 'text-indigo-500' : 'text-gray-400 dark:text-gray-600'}`} />
          <ComboboxInput
            className={`w-28 bg-transparent outline-none placeholder:text-gray-500 dark:placeholder:text-gray-400 ${
              value ? 'text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'
            }`}
            displayValue={() => selected?.label ?? ''}
            placeholder={placeholder}
            onChange={(e) => setQuery(e.target.value)}
            onFocus={() => setQuery('')}
          />
          <ComboboxButton className="shrink-0">
            <ChevronDown className="h-3.5 w-3.5 text-gray-400 dark:text-gray-600" />
          </ComboboxButton>
        </div>

        <ComboboxOptions
          anchor="bottom start"
          transition
          className="z-20 mt-1 max-h-72 w-[var(--input-width)] min-w-48 origin-top overflow-auto rounded-lg border border-gray-200 bg-white p-1 text-xs shadow-lg transition duration-100 ease-out [--anchor-gap:4px] empty:invisible focus:outline-none data-[closed]:scale-95 data-[closed]:opacity-0 dark:border-gray-800 dark:bg-gray-900"
        >
          {query === '' && (
            <ComboboxOption
              value=""
              className="flex cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-gray-500 select-none data-[focus]:bg-gray-100 dark:text-gray-400 dark:data-[focus]:bg-gray-800"
            >
              <Check className={`h-3.5 w-3.5 shrink-0 ${value ? 'invisible' : 'text-indigo-500'}`} />
              {placeholder}
            </ComboboxOption>
          )}
          {filtered.map((o) => (
            <ComboboxOption
              key={o.value}
              value={o.value}
              className="group flex cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-gray-700 select-none data-[focus]:bg-gray-100 data-[selected]:font-medium data-[selected]:text-gray-900 dark:text-gray-300 dark:data-[focus]:bg-gray-800 dark:data-[selected]:text-white"
            >
              <Check className="invisible h-3.5 w-3.5 shrink-0 text-indigo-500 group-data-[selected]:visible" />
              <span className="truncate">{o.label}</span>
            </ComboboxOption>
          ))}
          {filtered.length === 0 && (
            <div className="px-2.5 py-1.5 text-gray-400 dark:text-gray-600">No matches</div>
          )}
        </ComboboxOptions>
      </div>
    </Combobox>
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
