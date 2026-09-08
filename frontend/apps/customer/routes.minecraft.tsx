import type { RouteObject } from 'react-router-dom'
import { MinecraftPage } from './pages/minecraft/Minecraft'
import { MinecraftDetailPage } from './pages/minecraft/MinecraftDetail'

/**
 * Phase 7 route fragment. The coordinator mounts this under the guarded
 * shell: export const routes: RouteObject[] (react-router v7 contract).
 */
export const routes: RouteObject[] = [
  { path: '/minecraft', element: <MinecraftPage /> },
  { path: '/minecraft/:instance_id', element: <MinecraftDetailPage /> },
]
