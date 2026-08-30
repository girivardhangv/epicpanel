# shared/

Cross-component contracts that must stay in lockstep across `frontend`,
`backend`, and `agent`.

Today these live under version control here:

- **Agent ↔ Panel protocol** (register / heartbeat payloads) is defined by
  `agent/internal/client` requests mirrored in `backend/internal/servers`.
- **HTTP API surface** (paths, error codes, permission names): documented in
  `docs/api.md`, enforced server-side via RBAC seeds in migration
  `backend/migrations/0001_foundation.sql`.

As the platform grows, generated TypeScript types from Go structs and OpenAPI
descriptions of `/api/v1` will land in this directory so drift fails builds.
For the foundation phase we kept hand-mirrored types small and test-backed.
