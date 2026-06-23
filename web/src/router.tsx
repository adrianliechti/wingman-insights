import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  useSearch,
} from '@tanstack/react-router'
import { DashProvider, validateSearch } from './dash'
import type { DashSearch } from './dash'
import { Header } from './components/Header'
import { FilterBar } from './components/FilterBar'
import { Overview } from './pages/Overview'
import { Traces } from './pages/Traces'
import { Product } from './pages/Product'
import { Operations } from './pages/Operations'
import { Finops } from './pages/Finops'

function Layout() {
  const search = useSearch({ strict: false }) as DashSearch
  return (
    <div className="min-h-screen bg-gray-50 text-gray-900 dark:bg-gray-950 dark:text-gray-100">
      <div className="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
        <DashProvider search={search}>
          <Header />
          <FilterBar />
          <Outlet />
        </DashProvider>
      </div>
    </div>
  )
}

const rootRoute = createRootRoute({
  component: Layout,
  validateSearch,
})

const pages = [
  { path: '/', component: Overview },
  { path: '/traces', component: Traces },
  { path: '/product', component: Product },
  { path: '/operations', component: Operations },
  { path: '/finops', component: Finops },
]

const routeTree = rootRoute.addChildren(
  pages.map((p) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path: p.path,
      component: p.component,
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
  return result
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
