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
import { Anomalies } from './pages/Anomalies'
import { Costs } from './pages/Costs'
import { Users } from './pages/Users'
import { Http } from './pages/Http'

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
  { path: '/anomalies', component: Anomalies },
  { path: '/costs', component: Costs },
  { path: '/users', component: Users },
  { path: '/http', component: Http },
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

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
