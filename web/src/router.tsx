import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  useLocation,
  useSearch,
} from '@tanstack/react-router'
import { DashProvider, MeProvider, useMe, validateSearch } from './dash'
import type { ReactNode } from 'react'
import type { DashSearch } from './dash'
import { Header } from './components/Header'
import { FilterBar } from './components/FilterBar'
import { Overview } from './pages/Overview'
import { Personal } from './pages/Personal'
import { Customers } from './pages/Customers'
import { Operations } from './pages/Operations'
import { Finops } from './pages/Finops'

// Shell decides what the authenticated caller may see. Admins get the full
// org-wide dashboard (nav + the routed page); everyone else sees only their
// personal usage, with no nav or filter bar, whatever route they land on. The
// filter bar is also hidden on the personal view for admins, since the personal
// endpoints are scoped to the caller and ignore those filters.
function Shell() {
  const { me, loading } = useMe()
  const { pathname } = useLocation()
  if (loading) {
    return <div className="py-20 text-center text-sm text-gray-400 dark:text-gray-600">Loading…</div>
  }
  if (!me?.admin) {
    return (
      <>
        <Header personalOnly />
        <Personal />
      </>
    )
  }
  const isPersonal = pathname === '/' || pathname.endsWith('/personal')
  return (
    <>
      <Header personalRange={isPersonal} />
      {!isPersonal && <FilterBar />}
      <Outlet />
    </>
  )
}

function Layout() {
  const search = useSearch({ strict: false }) as DashSearch
  return (
    <div className="min-h-screen bg-gray-50 text-gray-900 dark:bg-gray-950 dark:text-gray-100">
      <div className="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
        <DashProvider search={search}>
          <MeProvider>
            <Shell />
          </MeProvider>
        </DashProvider>
      </div>
    </div>
  )
}

const rootRoute = createRootRoute({
  component: Layout,
  validateSearch,
})

// Routes are declared with a widened `path: string` so the router's link types
// stay permissive app-wide; adding them as literal entries narrows the type
// registry to just those literals and makes every other <Link to=…> fail.
const pages: {
  path: string
  component: () => ReactNode
  beforeLoad?: (ctx: { search: DashSearch }) => void
}[] = [
  { path: '/', component: Personal },
  { path: '/overview', component: Overview },
  { path: '/customers', component: Customers },
  { path: '/finops', component: Finops },
  { path: '/operations', component: Operations },
  // Pages that became sub-views of a section (see nav.ts) keep their old paths
  // as redirects, so existing links and bookmarks still work and carry the
  // current range and filters across. component never renders — beforeLoad
  // throws first — and is only present to keep the entries uniformly typed.
  ...[
    { path: '/traces', to: '/operations', view: 'traces' },
    { path: '/classification', to: '/customers', view: 'classification' },
    { path: '/anomalies', to: '/customers', view: 'anomalies' },
  ].map(({ path, to, view }) => ({
    path,
    component: Operations,
    beforeLoad: ({ search }: { search: DashSearch }) => {
      throw redirect({ to, search: { ...search, view } })
    },
  })),
]

const routeTree = rootRoute.addChildren(
  pages.map((p) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path: p.path,
      component: p.component,
      beforeLoad: p.beforeLoad,
    }),
  ),
)

const baseEl = document.querySelector('base')
const basepath = baseEl
  ? new URL(baseEl.href).pathname.replace(/\/$/, '') || '/'
  : '/'

function parseSearch(search: string): Record<string, unknown> {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  const result: Record<string, unknown> = {}
  for (const [key, value] of params.entries()) {
    result[key] = value
  }
  // Normalize to validateSearch's shape (e.g. app as string[]) here, so
  // router.state.location.search always matches what Link's active-state
  // check builds via validateSearch — otherwise a raw string vs. array
  // mismatch makes every nav Link with a filter param look inactive.
  return validateSearch(result) as Record<string, unknown>
}

function stringifySearch(search: Record<string, unknown>): string {
  const params: string[] = []
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined) continue
    const rawValue = Array.isArray(value) ? value.filter(Boolean).join(',') : String(value)
    if (!rawValue) continue
    const encodedValue = encodeURIComponent(rawValue).replaceAll('%2C', ',')
    params.push(`${encodeURIComponent(key)}=${encodedValue}`)
  }
  return params.length > 0 ? `?${params.join('&')}` : ''
}

export const router = createRouter({ routeTree, basepath, parseSearch, stringifySearch })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
