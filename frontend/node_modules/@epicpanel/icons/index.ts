// Icon policy (global contract #7): one icon library (lucide-react), no emoji.
// This package re-exports the icon set so app code never imports the vendor
// directly and icon choices stay centralized.
import type { ComponentType } from 'react'

export * from 'lucide-react'

/** Shape every nav/menu consumer expects from an icon component. */
export type IconComponent = ComponentType<{
  size?: number | string
  className?: string
  strokeWidth?: number
}>
