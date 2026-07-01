import { useDeferredValue, useMemo, useState } from 'react'
import {
  Combobox,
  ComboboxButton,
  ComboboxInput,
  ComboboxOption,
  ComboboxOptions,
} from '@headlessui/react'
import { Bot, Boxes, Building2, Check, ChevronDown, Cpu, FilterX, MapPin, User } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useApi, useDash, useFilterNav } from '../dash'
import type { FilterOptions } from '../types'

const MAX_VISIBLE_OPTIONS = 100

type SelectOption = { value: string; label: string }
type SearchableOption = SelectOption & { searchLabel: string }

function useOptionMatches(options: SelectOption[], query: string, selectedValues: string[] = []) {
  const deferredQuery = useDeferredValue(query)
  const normalizedQuery = deferredQuery.trim().toLowerCase()
  const searchableOptions = useMemo<SearchableOption[]>(
    () => options.map((o) => ({ ...o, searchLabel: o.label.toLowerCase() })),
    [options],
  )
  const selectedOptions = useMemo(
    () =>
      selectedValues
        .map((v) => searchableOptions.find((o) => o.value === v))
        .filter((o): o is SearchableOption => !!o),
    [searchableOptions, selectedValues],
  )
  const filtered = useMemo(
    () =>
      normalizedQuery === ''
        ? searchableOptions
        : searchableOptions.filter((o) => o.searchLabel.includes(normalizedQuery)),
    [normalizedQuery, searchableOptions],
  )
  const visibleOptions = useMemo(() => {
    const firstOptions = filtered.slice(0, MAX_VISIBLE_OPTIONS)
    const visibleValues = new Set(firstOptions.map((o) => o.value))
    const filteredValues = new Set(filtered.map((o) => o.value))
    const pinnedOptions = selectedOptions.filter((o) => filteredValues.has(o.value) && !visibleValues.has(o.value))
    return [...pinnedOptions, ...firstOptions].slice(0, MAX_VISIBLE_OPTIONS)
  }, [filtered, selectedOptions])

  return {
    filtered,
    hiddenCount: filtered.length - visibleOptions.length,
    selectedOptions,
    visibleOptions,
  }
}

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
  options: SelectOption[]
  onChange: (v: string | undefined) => void
}) {
  const [query, setQuery] = useState('')
  const { filtered, hiddenCount, selectedOptions, visibleOptions } = useOptionMatches(
    options,
    query,
    value ? [value] : [],
  )
  const selected = selectedOptions[0]

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
          {visibleOptions.map((o) => (
            <ComboboxOption
              key={o.value}
              value={o.value}
              className="group flex cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-gray-700 select-none data-[focus]:bg-gray-100 data-[selected]:font-medium data-[selected]:text-gray-900 dark:text-gray-300 dark:data-[focus]:bg-gray-800 dark:data-[selected]:text-white"
            >
              <Check className="invisible h-3.5 w-3.5 shrink-0 text-indigo-500 group-data-[selected]:visible" />
              <span className="truncate">{o.label}</span>
            </ComboboxOption>
          ))}
          {hiddenCount > 0 && (
            <div className="px-2.5 py-1.5 text-gray-400 dark:text-gray-600">
              Showing {visibleOptions.length.toLocaleString()} of {filtered.length.toLocaleString()}
            </div>
          )}
          {filtered.length === 0 && (
            <div className="px-2.5 py-1.5 text-gray-400 dark:text-gray-600">No matches</div>
          )}
        </ComboboxOptions>
      </div>
    </Combobox>
  )
}

function MultiFilterSelect({
  icon: Icon,
  placeholder,
  value,
  options,
  onChange,
}: {
  icon: LucideIcon
  placeholder: string
  value: string[] | undefined
  options: SelectOption[]
  onChange: (v: string[] | undefined) => void
}) {
  const values = value ?? []
  const [query, setQuery] = useState('')
  const { filtered, hiddenCount, selectedOptions, visibleOptions } = useOptionMatches(options, query, values)
  const selectedLabel =
    selectedOptions.length === 0 ? ''
    : selectedOptions.length === 1 ? selectedOptions[0].label
    : `${selectedOptions.length} models`

  function setValues(next: string[]) {
    onChange(next.length > 0 ? next : undefined)
    setQuery('')
  }

  return (
    <Combobox multiple value={values} onChange={setValues}>
      <div className="relative">
        <div
          className={`flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors ${
            values.length > 0
              ? 'border-indigo-300 bg-indigo-50 dark:border-indigo-500/40 dark:bg-indigo-500/10'
              : 'border-gray-200 bg-white hover:border-gray-300 dark:border-gray-800 dark:bg-gray-900 dark:hover:border-gray-700'
          }`}
        >
          <Icon
            className={`h-3.5 w-3.5 shrink-0 ${
              values.length > 0 ? 'text-indigo-500' : 'text-gray-400 dark:text-gray-600'
            }`}
          />
          <ComboboxInput
            className={`w-28 bg-transparent outline-none placeholder:text-gray-500 dark:placeholder:text-gray-400 ${
              values.length > 0 ? 'text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'
            }`}
            displayValue={() => selectedLabel}
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
            <button
              type="button"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => setValues([])}
              className="flex w-full cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-gray-500 select-none hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-800"
            >
              <Check className={`h-3.5 w-3.5 shrink-0 ${values.length > 0 ? 'invisible' : 'text-indigo-500'}`} />
              {placeholder}
            </button>
          )}
          {visibleOptions.map((o) => (
            <ComboboxOption
              key={o.value}
              value={o.value}
              className="group flex cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-gray-700 select-none data-[focus]:bg-gray-100 data-[selected]:font-medium data-[selected]:text-gray-900 dark:text-gray-300 dark:data-[focus]:bg-gray-800 dark:data-[selected]:text-white"
            >
              <Check className="invisible h-3.5 w-3.5 shrink-0 text-indigo-500 group-data-[selected]:visible" />
              <span className="truncate">{o.label}</span>
            </ComboboxOption>
          ))}
          {hiddenCount > 0 && (
            <div className="px-2.5 py-1.5 text-gray-400 dark:text-gray-600">
              Showing {visibleOptions.length.toLocaleString()} of {filtered.length.toLocaleString()}
            </div>
          )}
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
  const setFilter = useFilterNav()
  // The options list itself must not shrink to the current selection, so this
  // request carries only the time range, not the active filters.
  const { data } = useApi<FilterOptions>('/api/filters', {
    app: undefined,
    user: undefined,
    department: undefined,
    location: undefined,
    provider: undefined,
    models: undefined,
    interval: undefined,
  })

  const hasFilter = !!(
    search.app ||
    search.user ||
    search.department ||
    search.location ||
    search.provider ||
    (search.models?.length ?? 0) > 0
  )
  const appOptions = useMemo(
    () => (data?.apps ?? []).map((a) => ({ value: a.id, label: a.name || a.id })),
    [data?.apps],
  )
  const userOptions = useMemo(
    () => (data?.users ?? []).map((u) => ({ value: u.id, label: u.name || u.id })),
    [data?.users],
  )
  const departmentOptions = useMemo(
    () => (data?.departments ?? []).map((d) => ({ value: d, label: d })),
    [data?.departments],
  )
  const locationOptions = useMemo(
    () => (data?.locations ?? []).map((l) => ({ value: l, label: l })),
    [data?.locations],
  )
  const providerOptions = useMemo(
    () => (data?.providers ?? []).map((p) => ({ value: p, label: p })),
    [data?.providers],
  )
  const modelOptions = useMemo(
    () => (data?.models ?? []).map((m) => ({ value: m, label: m })),
    [data?.models],
  )

  return (
    <div className="flex flex-wrap items-center gap-2 py-4">
      <FilterSelect
        icon={Boxes}
        placeholder="All applications"
        value={search.app}
        options={appOptions}
        onChange={(v) => setFilter({ app: v })}
      />
      <FilterSelect
        icon={User}
        placeholder="All users"
        value={search.user}
        options={userOptions}
        onChange={(v) => setFilter({ user: v })}
      />
      {departmentOptions.length > 0 && (
        <FilterSelect
          icon={Building2}
          placeholder="All departments"
          value={search.department}
          options={departmentOptions}
          onChange={(v) => setFilter({ department: v })}
        />
      )}
      {locationOptions.length > 0 && (
        <FilterSelect
          icon={MapPin}
          placeholder="All locations"
          value={search.location}
          options={locationOptions}
          onChange={(v) => setFilter({ location: v })}
        />
      )}
      <FilterSelect
        icon={Bot}
        placeholder="All providers"
        value={search.provider}
        options={providerOptions}
        onChange={(v) => setFilter({ provider: v })}
      />
      <MultiFilterSelect
        icon={Cpu}
        placeholder="All models"
        value={search.models}
        options={modelOptions}
        onChange={(v) => setFilter({ models: v })}
      />
      {hasFilter && (
        <button
          onClick={() =>
            setFilter({
              app: undefined,
              user: undefined,
              department: undefined,
              location: undefined,
              provider: undefined,
              models: undefined,
            })
          }
          className="flex items-center gap-1 rounded-lg px-2 py-1.5 text-xs font-medium text-gray-500 hover:text-gray-900 dark:hover:text-white"
        >
          <FilterX className="h-3.5 w-3.5" />
          Clear
        </button>
      )}
    </div>
  )
}
