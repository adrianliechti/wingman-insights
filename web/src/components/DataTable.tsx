import { useState } from 'react'
import {
  createSortedRowModel,
  flexRender,
  metaHelper,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  sortFn_datetime,
  sortFn_text,
  tableFeatures,
  useTable,
} from '@tanstack/react-table'
import type { ColumnDef, RowData, SortingState } from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react'
import { PanelLoadingRows, Stale } from './Panel'

// align is the only per-column extra these tables carry; declaring it as the
// table's columnMeta slot types `meta: { align: 'right' }` at every call site.
type ColumnMeta = { align?: 'right' }

// Sorting is the only table feature in use, so register just that one plus its
// row model. The sortFns registry is what `sortFn: 'auto'` resolves against —
// without it, auto-detection cannot find the built-ins and every column falls
// back to the basic comparator, which would reorder the text columns.
const features = tableFeatures({
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: {
    alphanumeric: sortFn_alphanumeric,
    basic: sortFn_basic,
    datetime: sortFn_datetime,
    text: sortFn_text,
  },
  columnMeta: metaHelper<ColumnMeta>(),
})

// DataTableColumns keeps the feature set in this module: callers describe their
// columns without naming the table's features themselves.
export type DataTableColumns<T extends RowData> = ColumnDef<typeof features, T, any>[]

// showMoreStep is how many extra rows each "Show more" click reveals on a
// limited table.
const showMoreStep = 100

export function DataTable<T extends RowData>({
  data,
  columns,
  initialSort,
  onRowClick,
  initialLimit,
  loading,
}: {
  data: T[]
  columns: DataTableColumns<T>
  initialSort?: SortingState
  onRowClick?: (row: T) => void
  // initialLimit caps how many (sorted) rows render initially, with a
  // "Show more / Show all" footer for the rest — for tables whose population is
  // unbounded (users, departments, routes) and would otherwise render thousands
  // of DOM rows. Unset renders everything.
  initialLimit?: number
  // loading marks the rows as not matching the current filters yet: placeholder
  // rows before there is anything to show, dimmed existing rows during a
  // refetch. Optional, so callers that never refetch can ignore it.
  loading?: boolean
}) {
  const [sorting, setSorting] = useState<SortingState>(initialSort ?? [])
  const [limit, setLimit] = useState(initialLimit)
  const table = useTable({
    features,
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
  })
  const allRows = table.getRowModel().rows
  const rows = limit ? allRows.slice(0, limit) : allRows
  const hidden = allRows.length - rows.length

  if (loading && data.length === 0) return <PanelLoadingRows />

  return (
    <Stale when={loading}>
      <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id} className="border-b border-gray-200 dark:border-gray-800">
              {hg.headers.map((header) => {
                const sorted = header.column.getIsSorted()
                const canSort = header.column.getCanSort()
                const align = header.column.columnDef.meta?.align === 'right' ? 'justify-end text-right' : 'text-left'
                return (
                  <th key={header.id} className="px-3 py-2">
                    <button
                      type="button"
                      disabled={!canSort}
                      onClick={header.column.getToggleSortingHandler()}
                      className={`flex w-full items-center gap-1 text-xs font-medium uppercase tracking-wider text-gray-500 ${align} ${
                        canSort ? 'cursor-pointer hover:text-gray-700 dark:hover:text-gray-300' : ''
                      }`}
                    >
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {canSort &&
                        (sorted === 'asc' ? (
                          <ArrowUp className="h-3 w-3 shrink-0" />
                        ) : sorted === 'desc' ? (
                          <ArrowDown className="h-3 w-3 shrink-0" />
                        ) : (
                          <ArrowUpDown className="h-3 w-3 shrink-0 opacity-40" />
                        ))}
                    </button>
                  </th>
                )
              })}
            </tr>
          ))}
        </thead>
        <tbody className="divide-y divide-gray-100 dark:divide-gray-800/50">
          {rows.map((row) => (
            <tr
              key={row.id}
              onClick={onRowClick ? () => onRowClick(row.original) : undefined}
              className={`transition-colors hover:bg-gray-50 dark:hover:bg-gray-800/30 ${onRowClick ? 'cursor-pointer' : ''}`}
            >
              {/* getAllCells rather than getVisibleCells: column visibility is
                  not one of the registered features, so every cell is visible
                  and the core accessor is the one available. */}
              {row.getAllCells().map((cell) => {
                const align = cell.column.columnDef.meta?.align === 'right' ? 'text-right' : 'text-left'
                return (
                  <td key={cell.id} className={`px-3 py-2.5 tabular-nums text-gray-700 dark:text-gray-300 ${align}`}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {hidden > 0 && (
        <div className="flex items-center justify-center gap-3 border-t border-gray-100 py-2 text-xs dark:border-gray-800/50">
          <span className="text-gray-400 dark:text-gray-500">
            Showing {rows.length.toLocaleString()} of {allRows.length.toLocaleString()}
          </span>
          <button
            type="button"
            onClick={() => setLimit((l) => (l ?? 0) + showMoreStep)}
            className="cursor-pointer font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
          >
            Show more
          </button>
          <button
            type="button"
            onClick={() => setLimit(undefined)}
            className="cursor-pointer font-medium text-gray-500 hover:text-gray-900 dark:hover:text-white"
          >
            Show all
          </button>
        </div>
      )}
      </div>
    </Stale>
  )
}
