import { useMemo, useState } from 'react'
import { ArrowDown, ChevronLeft, ChevronRight, Search } from 'lucide-react'
import type { ClassificationDepartmentTopicCell } from '../types'
import { departmentTopicRows, topicShare } from '../lib/departmentTopics'
import { PanelMessage, Stale } from './Panel'

const pageSize = 15
const pretty = (id: string) => id.replace(/[_-]+/g, ' ')
const label = (department: string) => department || 'Unassigned'

export function DepartmentTopics({
  cells,
  onSelectDepartment,
  loading,
}: {
  cells: ClassificationDepartmentTopicCell[]
  onSelectDepartment: (department: string) => void
  loading?: boolean
}) {
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState('volume')
  const [page, setPage] = useState(0)
  const { departments, topics } = useMemo(() => departmentTopicRows(cells), [cells])
  const activeSort = sort.startsWith('topic:') && !topics.includes(sort.slice(6)) ? 'volume' : sort
  const rows = useMemo(() => {
    const search = query.trim().toLocaleLowerCase()
    const filtered = departments.filter((row) => label(row.department).toLocaleLowerCase().includes(search))
    return filtered.sort((a, b) => {
      const nameOrder = label(a.department).localeCompare(label(b.department))
      if (activeSort === 'name') return nameOrder
      if (activeSort.startsWith('topic:')) {
        const topic = activeSort.slice(6)
        const difference = topicShare(b, topic) - topicShare(a, topic)
        if (difference !== 0) return difference
      }
      return b.total - a.total || nameOrder
    })
  }, [departments, query, activeSort])
  const lastPage = Math.max(0, Math.ceil(rows.length / pageSize) - 1)
  const currentPage = Math.min(page, lastPage)
  const shown = rows.slice(currentPage * pageSize, (currentPage + 1) * pageSize)
  const changeSort = (value: string) => {
    setSort(value)
    setPage(0)
  }

  if (departments.length === 0) return <PanelMessage>No classified activity in range</PanelMessage>

  return (
    <Stale when={loading}>
      <div>
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div className="relative w-full sm:w-72">
            <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-gray-400" />
            <input
              aria-label="Search departments"
              type="search"
              value={query}
              onChange={(event) => { setQuery(event.target.value); setPage(0) }}
              placeholder={`Search ${departments.length.toLocaleString()} departments…`}
              className="w-full rounded-md border border-gray-200 bg-transparent py-2 pl-9 pr-3 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none dark:border-gray-700 dark:text-gray-100"
            />
          </div>
          <label className="flex items-center gap-2 text-xs text-gray-500">
            Sort by
            <select
              value={activeSort}
              onChange={(event) => changeSort(event.target.value)}
              className="max-w-64 rounded-md border border-gray-200 bg-white px-2 py-2 text-xs text-gray-700 focus:outline-indigo-500 dark:border-gray-700 dark:bg-gray-950 dark:text-gray-200"
            >
              <option value="volume">Prompt volume</option>
              <option value="name">Department name</option>
              {topics.map((topic) => <option key={topic} value={`topic:${topic}`}>{pretty(topic)} share</option>)}
            </select>
          </label>
        </div>

        {shown.length === 0 ? (
          <PanelMessage>No departments match “{query}”</PanelMessage>
        ) : (
          <div className="overflow-x-auto rounded-md border border-gray-100 dark:border-gray-800">
            <table className="w-full border-separate border-spacing-0 text-xs">
              <caption className="sr-only">Topic shares for every department, paginated and searchable. Each percentage is a share of that department’s prompts.</caption>
              <thead>
                <tr className="text-gray-500">
                  <th scope="col" className="sticky left-0 z-10 min-w-56 bg-white px-3 py-3 text-left font-medium dark:bg-gray-950">Department</th>
                  <th scope="col" className="px-3 py-3 text-right font-medium">Prompts</th>
                  {topics.map((topic) => (
                    <th key={topic} scope="col" aria-sort={activeSort === `topic:${topic}` ? 'descending' : 'none'} className="min-w-24 max-w-36 px-1 py-2 text-center font-medium">
                      <button
                        type="button"
                        onClick={() => changeSort(`topic:${topic}`)}
                        title={`Rank departments by their share of ${pretty(topic)} prompts`}
                        className="inline-flex cursor-pointer items-center justify-center gap-1 rounded px-1 py-1 text-gray-600 hover:text-indigo-600 dark:text-gray-300 dark:hover:text-indigo-400"
                      >
                        {pretty(topic)}
                        {activeSort === `topic:${topic}` && <ArrowDown className="h-3 w-3 shrink-0" />}
                      </button>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {shown.map((row) => (
                  <tr key={row.department}>
                    <th scope="row" className="sticky left-0 z-10 border-t border-gray-100 bg-white px-3 py-2 text-left font-medium text-gray-700 dark:border-gray-800 dark:bg-gray-950 dark:text-gray-200">
                      {row.department ? (
                        <button type="button" onClick={() => onSelectDepartment(row.department)} title={`Filter dashboard to ${row.department}`} className="block max-w-72 cursor-pointer truncate rounded text-left hover:text-indigo-600 dark:hover:text-indigo-400">
                          {row.department}
                        </button>
                      ) : <span className="text-gray-400">Unassigned</span>}
                    </th>
                    <td className="border-t border-gray-100 px-3 py-2 text-right tabular-nums text-gray-500 dark:border-gray-800">{row.total.toLocaleString()}</td>
                    {topics.map((topic) => {
                      const count = row.topics.get(topic) ?? 0
                      const share = topicShare(row, topic)
                      return (
                        <td key={topic} className="border-t border-gray-100 p-1 dark:border-gray-800">
                          <div
                            title={`${label(row.department)} · ${pretty(topic)}: ${count.toLocaleString()} of ${row.total.toLocaleString()} prompts (${(share * 100).toFixed(1)}%)`}
                            className={`flex h-8 items-center justify-center rounded text-[11px] tabular-nums ${count === 0 ? 'bg-gray-50 text-gray-300 dark:bg-gray-800/40 dark:text-gray-600' : 'text-gray-900 dark:text-gray-100'}`}
                            style={count > 0 ? { backgroundColor: `rgba(66, 87, 168, ${0.08 + share * 0.57})` } : undefined}
                          >
                            {count === 0 ? '—' : share < 0.01 ? '<1%' : `${Math.round(share * 100)}%`}
                          </div>
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <div className="mt-3 flex flex-wrap items-center justify-between gap-3 text-xs text-gray-500">
          <span>{rows.length > 0 ? `${(currentPage * pageSize + 1).toLocaleString()}–${Math.min((currentPage + 1) * pageSize, rows.length).toLocaleString()} of ${rows.length.toLocaleString()} departments` : '0 departments'}{query.trim() ? ` · ${departments.length.toLocaleString()} total` : ''}</span>
          <div className="flex items-center gap-2">
            <span>Topic share</span>
            <span>0%</span>
            <span aria-hidden="true" className="h-2 w-20 rounded" style={{ background: 'linear-gradient(to right, rgba(66,87,168,.08), rgba(66,87,168,.65))' }} />
            <span>100%</span>
          </div>
          <div className="flex items-center gap-2">
            <button type="button" aria-label="Previous departments" disabled={currentPage === 0} onClick={() => setPage(currentPage - 1)} className="cursor-pointer rounded border border-gray-200 p-1.5 hover:bg-gray-50 disabled:cursor-default disabled:opacity-30 dark:border-gray-700 dark:hover:bg-gray-800"><ChevronLeft className="h-4 w-4" /></button>
            <span className="tabular-nums">{currentPage + 1} / {lastPage + 1}</span>
            <button type="button" aria-label="Next departments" disabled={currentPage === lastPage} onClick={() => setPage(currentPage + 1)} className="cursor-pointer rounded border border-gray-200 p-1.5 hover:bg-gray-50 disabled:cursor-default disabled:opacity-30 dark:border-gray-700 dark:hover:bg-gray-800"><ChevronRight className="h-4 w-4" /></button>
          </div>
        </div>
        <p className="mt-3 text-[11px] text-gray-400">Each row is one department. Compare topic shares across teams; prompt counts show how much activity each percentage represents. Click a topic to rank departments, or a department to filter the dashboard.</p>
      </div>
    </Stale>
  )
}
