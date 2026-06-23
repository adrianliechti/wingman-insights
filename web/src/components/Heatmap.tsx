import type { ReactNode } from 'react'

export interface HeatCell {
  bg: string
  text?: string
  title?: string
  content?: ReactNode
}

// Heatmap renders a labelled grid (rows × cols) where each cell's color encodes
// a value. It backs both the cohort-retention matrix and the spike heatmap; the
// caller supplies the color/label per cell. A null cell renders as an empty slot.
export function Heatmap<R, C>({
  rows,
  cols,
  corner,
  rowHeader,
  colHeader,
  cell,
}: {
  rows: R[]
  cols: C[]
  corner?: ReactNode
  rowHeader: (r: R) => ReactNode
  colHeader: (c: C) => ReactNode
  cell: (r: R, c: C) => HeatCell | null
}) {
  return (
    <div className="overflow-x-auto">
      <table className="border-separate border-spacing-1 text-xs">
        <thead>
          <tr>
            <th className="px-2 py-1 text-left text-[11px] font-medium uppercase tracking-wider text-gray-500">
              {corner}
            </th>
            {cols.map((c, i) => (
              <th key={i} className="px-1 py-1 text-center text-[11px] font-medium text-gray-500">
                {colHeader(c)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, ri) => (
            <tr key={ri}>
              <td className="whitespace-nowrap px-2 py-1 text-xs text-gray-600 dark:text-gray-300">{rowHeader(r)}</td>
              {cols.map((c, ci) => {
                const cl = cell(r, c)
                return (
                  <td key={ci} className="p-0">
                    {cl ? (
                      <div
                        title={cl.title}
                        className="flex h-8 min-w-[2.75rem] items-center justify-center rounded text-[11px] font-medium tabular-nums"
                        style={{ backgroundColor: cl.bg, color: cl.text }}
                      >
                        {cl.content}
                      </div>
                    ) : (
                      <div className="h-8 min-w-[2.75rem] rounded bg-gray-100 dark:bg-gray-800/40" />
                    )}
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
