import type { ClassificationDepartmentTopicCell } from '../types'

export interface DepartmentTopicRow {
  department: string
  total: number
  topics: Map<string, number>
}

// Keep each department separate: combining the tail would hide the differences
// this comparison is meant to show. Topic order stays consistent across pages.
export function departmentTopicRows(cells: ClassificationDepartmentTopicCell[]) {
  const departments = new Map<string, DepartmentTopicRow>()
  const topicTotals = new Map<string, number>()
  for (const cell of cells) {
    if (cell.prompts <= 0) continue
    let row = departments.get(cell.department)
    if (!row) {
      row = { department: cell.department, total: 0, topics: new Map() }
      departments.set(cell.department, row)
    }
    row.total += cell.prompts
    row.topics.set(cell.category, (row.topics.get(cell.category) ?? 0) + cell.prompts)
    topicTotals.set(cell.category, (topicTotals.get(cell.category) ?? 0) + cell.prompts)
  }
  const topics = [...topicTotals.keys()].sort(
    (a, b) => topicTotals.get(b)! - topicTotals.get(a)! || a.localeCompare(b),
  )
  return { departments: [...departments.values()], topics }
}

export function topicShare(row: DepartmentTopicRow, topic: string) {
  return row.total > 0 ? (row.topics.get(topic) ?? 0) / row.total : 0
}
