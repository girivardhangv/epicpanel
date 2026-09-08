import type { RouteObject } from 'react-router-dom'
import { BotsPage } from './pages/bots/BotsList'
import { BotDetailPage } from './pages/bots/BotDetail'

/**
 * Phase 8 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 */
export const routes: RouteObject[] = [
  { path: '/bots', element: <BotsPage /> },
  { path: '/bots/:bot_id', element: <BotDetailPage /> },
]
