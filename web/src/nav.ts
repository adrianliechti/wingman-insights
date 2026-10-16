// Navigation structure for the org-wide dashboard: the top-level sections and,
// where a section has more than one view, its sub-views.
//
// Related views live together under one section rather than as separate
// top-level entries, so the main nav stays short and the pages that answer the
// same question sit side by side. A sub-view is selected by the `view` search
// param (see DashSearch), which keeps it linkable, bookmarkable and part of the
// browser's history — the Header renders the submenu, and each page renders the
// view resolved from that param.
//
// The first view of a section is its default and renders with no `view` param
// at all, so the common URL stays clean (/operations, not
// /operations?view=metrics).

export interface NavView {
  readonly id: string
  readonly label: string
}

export interface NavItem {
  readonly to: string
  readonly label: string
  readonly views?: readonly NavView[]
}

// Operations: aggregate service health, and the per-request drill-down.
export const opsViews = [
  { id: 'metrics', label: 'Metrics' },
  { id: 'traces', label: 'Traces' },
] as const
export type OpsView = (typeof opsViews)[number]['id']

// Customers: who is using the platform, what they use it for, and where their
// consumption looks abnormal.
export const customerViews = [
  { id: 'engagement', label: 'Engagement' },
  { id: 'classification', label: 'Classification' },
  { id: 'anomalies', label: 'Anomalies' },
] as const
export type CustomerView = (typeof customerViews)[number]['id']

export const NAV: readonly NavItem[] = [
  { to: '/', label: 'Personal' },
  { to: '/overview', label: 'Overview' },
  { to: '/customers', label: 'Customers', views: customerViews },
  { to: '/finops', label: 'FinOps' },
  { to: '/operations', label: 'Operations', views: opsViews },
]

// resolveView returns the active sub-view: the `view` search param when it names
// one of the section's views, else the section's default. An unknown value falls
// back to the default rather than rendering nothing, so a stale or hand-edited
// URL still shows a page.
export function resolveView<T extends NavView>(views: readonly T[], view?: string): T['id'] {
  return views.find((v) => v.id === view)?.id ?? views[0].id
}

// viewSearch patches the `view` param for a link to one of a section's views.
// The default view clears the param instead of spelling it out.
export function viewSearch<T extends NavView>(views: readonly T[], id: T['id']) {
  return id === views[0].id ? undefined : id
}
