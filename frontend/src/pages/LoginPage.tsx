import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { authApi } from "../services";
import { ApiError } from "../lib/api";
import { AuthProvider, useAuth } from "../features/auth/AuthContext";
import { Button } from "../components/ui/Button";
import { Input, PasswordInput } from "../components/ui/Input";
import { Alert } from "../components/ui/Alert";
import { Logo } from "../layouts/AppLayout";

const loginSchema = z.object({
  identifier: z.string().min(1, "Username or email is required"),
  password: z.string().min(1, "Password is required"),
});

type LoginForm = z.infer<typeof loginSchema>;

function LoginCard() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { isAuthenticated } = useAuth();
  const [error, setError] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<LoginForm>({ resolver: zodResolver(loginSchema) });

  const loginMutation = useMutation({
    mutationFn: (data: LoginForm) => authApi.login(data.identifier, data.password),
    onSuccess: async () => {
      setError(null);
      await queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
      navigate("/app", { replace: true });
    },
    onError: (err) => {
      if (err instanceof ApiError) {
        // Deliberately generic for invalid credentials; the API never reveals
        // whether an account exists.
        setError(err.message);
      } else {
        setError("Unexpected error. Please try again.");
      }
    },
  });

  if (isAuthenticated) return <Navigate to="/app" replace />;

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-900 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <Logo />
          <h1 className="text-lg font-semibold text-white">Sign in to EpicPanel</h1>
          <p className="text-xs text-slate-400">Hosting management console</p>
        </div>

        <form
          onSubmit={handleSubmit((data) => loginMutation.mutate(data))}
          className="space-y-4 rounded-xl bg-white p-6 shadow-xl"
          noValidate
        >
          {error && <Alert tone="danger">{error}</Alert>}

          <Input
            label="Username or email"
            autoComplete="username"
            autoFocus
            error={errors.identifier?.message}
            {...register("identifier")}
          />

          <PasswordInput
            label="Password"
            autoComplete="current-password"
            error={errors.password?.message}
            {...register("password")}
          />

          <Button type="submit" className="w-full" loading={loginMutation.isPending}>
            Sign in
          </Button>
        </form>
      </div>
    </div>
  );
}

export function LoginPage() {
  return (
    <AuthProvider>
      <LoginCard />
    </AuthProvider>
  );
}
