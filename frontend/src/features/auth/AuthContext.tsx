import { createContext, useCallback, useContext, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authApi } from "../../services";
import type { Permission } from "../../types/api";

interface AuthState {
  user?: {
    id: string;
    username: string;
    email?: string | null;
    display_name?: string;
    permissions: string[];
  };
  isLoading: boolean;
  isAuthenticated: boolean;
  hasPermission: (p: Permission) => boolean;
  refresh: () => void;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();

  const meQuery = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => authApi.me(),
    retry: false,
    staleTime: 60_000,
  });

  const logoutMutation = useMutation({
    mutationFn: () => authApi.logout(),
    onSettled: () => {
      queryClient.setQueryData(["auth", "me"], undefined);
      queryClient.clear();
    },
  });

  const user = meQuery.data?.user;

  const hasPermission = useCallback(
    (perm: Permission) => Boolean(user?.permissions?.includes(perm)),
    [user],
  );

  const refresh = useCallback(
    () => void queryClient.invalidateQueries({ queryKey: ["auth", "me"] }),
    [queryClient],
  );

  const value = useMemo<AuthState>(
    () => ({
      user,
      isLoading: meQuery.isLoading,
      isAuthenticated: Boolean(user),
      hasPermission,
      refresh,
      logout: async () => {
        await logoutMutation.mutateAsync();
      },
    }),
    [user, meQuery.isLoading, hasPermission, refresh, logoutMutation],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
