import { useState } from 'react'
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
} from '@tanstack/react-table'
import type { ColumnDef, SortingState } from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react'

export function DataTable<T>({
  data,
  columns,
  initialSort,
  onRowClick,
}: {
  data: T[]
  columns: ColumnDef<T, any>[]
  initialSort?: SortingState
  onRowClick?: (row: T) => void
}) {
  const [sorting, setSorting] = useState<SortingState>(initialSort ?? [])
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id} className="border-b border-gray-200 dark:border-gray-800">
              {hg.headers.map((header) => {
                const sorted = header.column.getIsSorted()
                const canSort = header.column.getCanSort()
                const align = (header.column.columnDef.meta as any)?.align === 'right' ? 'justify-end text-right' : 'text-left'
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
          {table.getRowModel().rows.map((row) => (
            <tr
              key={row.id}
              onClick={onRowClick ? () => onRowClick(row.original) : undefined}
              className={`transition-colors hover:bg-gray-50 dark:hover:bg-gray-800/30 ${onRowClick ? 'cursor-pointer' : ''}`}
            >
              {row.getVisibleCells().map((cell) => {
                const align = (cell.column.columnDef.meta as any)?.align === 'right' ? 'text-right' : 'text-left'
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
    </div>
  )
}
