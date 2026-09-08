// Back-compat shim: the legacy root app now re-exports the shared packages.
// Pages keep importing from '@/lib/api' etc. while the ONE implementation
// lives in packages/core (consumed verbatim by apps/customer too).
export * from '@epicpanel/core'
