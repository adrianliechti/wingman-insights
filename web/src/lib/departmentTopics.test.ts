import { expect, it } from 'vitest'
import { departmentTopicRows, topicShare } from './departmentTopics'

it('keeps all 700 departments and compares shares independently of team size', () => {
  const cells = Array.from({ length: 700 }, (_, i) => [
    { department: `Team ${i}`, category: 'research', prompts: (i + 1) * 3 },
    { department: `Team ${i}`, category: 'writing', prompts: i + 1 },
  ]).flat()
  const { departments, topics } = departmentTopicRows(cells)
  expect(departments).toHaveLength(700)
  expect(topics).toEqual(['research', 'writing'])
  expect(departments.reduce((sum, row) => sum + row.total, 0)).toBe(cells.reduce((sum, cell) => sum + cell.prompts, 0))
  expect(topicShare(departments[0], 'research')).toBe(0.75)
  expect(topicShare(departments[699], 'research')).toBe(0.75)
  expect(departments[699].department).toBe('Team 699')
})

it('keeps unassigned departments distinct from real department names', () => {
  const { departments } = departmentTopicRows([
    { department: '', category: 'research', prompts: 2 },
    { department: 'Other', category: 'writing', prompts: 3 },
  ])
  expect(departments).toHaveLength(2)
  expect(departments.find((row) => row.department === '')?.total).toBe(2)
  expect(departments.find((row) => row.department === 'Other')?.total).toBe(3)
  expect(topicShare(departments[0], 'missing')).toBe(0)
})
