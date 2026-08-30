import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "../features/auth/AuthContext";
import { Spinner } from "./ui/States";
import { AppLayout } from "../layouts/AppLayout";
import { get } from "../lib/api";
import { useQuery } from "@tanstack/react-query";
import type { InstallerStatus } from "../types/api";

/** Decides between installer / auth flows from server-side installation state. */
export function BootstrapGate() {
  const bootQuery = useQuery({
    queryKey: ["system", "installer-status"],
    queryFn: () => get<InstallerStatus>("/installer/status"),
    staleTime: 30_000,
  });

  if (bootQuery.isLoading) {
    return (
      <div className="flex h-screen items-center justify-center bg-slate-50">
        <Spinner label="Checking installation state…" />
      </div>
    );
  }

  if (!bootQuery.data) {
    return (
      <div className="flex h-screen flex-col items-center justify-center gap-3 bg-slate-50 text-center">
        <h1 className="text-lg font-semibold">Panel unreachable</h1>
        <p className="max-w-sm text-sm text-slate-500">
          The EpicPanel API did not respond. Verify the backend service is running.
        </p>
      </div>
    );
  }

  return bootQuery.data.installed ? (
    <Navigate to="/login" replace />
  ) : (
    <Navigate to="/install" replace />
  );
}

/**
 * Guards the authenticated area. Session validity is enforced by the API
 * (401 -> redirect); frontend checks are UX only, never security.
 */
export function ProtectedArea() {
  const location = useLocation();
  const { isLoading, isAuthenticated } = useAuth();

  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center bg-slate-50">
        <Spinner label="Restoring session…" />
      </div>
    );
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  return (
    <AppLayout>
      <Outlet />
    </AppLayout>
  );
}
