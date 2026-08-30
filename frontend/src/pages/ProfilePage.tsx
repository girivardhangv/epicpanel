import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useMutation } from "@tanstack/react-query";
import { authApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import { Card, CardBody, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { PasswordInput } from "../components/ui/Input";
import { Alert } from "../components/ui/Alert";

const changeSchema = z
  .object({
    current_password: z.string().min(1, "Current password is required"),
    new_password: z.string().min(1, "New password is required"),
    confirm_password: z.string().min(1, "Please confirm the new password"),
  })
  .refine((v) => v.new_password === v.confirm_password, {
    path: ["confirm_password"],
    message: "Passwords do not match",
  });

type ChangeForm = z.infer<typeof changeSchema>;

export function ProfilePage() {
  const { user } = useAuth();
  const [result, setResult] = useState<{ tone: "success" | "danger"; text: string } | null>(null);

  const {
    register,
    handleSubmit,
    watch,
    reset,
    formState: { errors },
  } = useForm<ChangeForm>({ resolver: zodResolver(changeSchema) });

  const change = useMutation({
    mutationFn: (d: ChangeForm) => authApi.changePassword(d.current_password, d.new_password, d.confirm_password),
    onSuccess: () => {
      reset();
      setResult({
        tone: "success",
        text: "Password changed. Every other session has been signed out.",
      });
    },
    onError: (err) =>
      setResult({ tone: "danger", text: err instanceof Error ? err.message : "Change failed" }),
  });

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Profile</h1>
        <p className="mt-0.5 text-sm text-slate-500">Your account and security settings.</p>
      </div>

      {result && <Alert tone={result.tone} title={result.tone === "success" ? "Done" : undefined}>{result.text}</Alert>}

      <Card>
        <CardHeader title="Account" />
        <CardBody>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
            <Row label="Username" value={user?.username ?? "—"} mono />
            <Row label="Email" value={user?.email || "—"} />
            <Row label="Display name" value={user?.display_name || "—"} />
            <Row label="Permissions held" value={String(user?.permissions.length ?? 0)} />
          </dl>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Change password" subtitle="Other sessions are revoked automatically." />
        <CardBody>
          <form
            className="space-y-4"
            onSubmit={handleSubmit((d) => {
              setResult(null);
              change.mutate(d);
            })}
          >
            <PasswordInput label="Current password" autoComplete="current-password" error={errors.current_password?.message} {...register("current_password")} />
            <PasswordInput
              label="New password"
              showMeter
              autoComplete="new-password"
              hint="Minimum 12 characters; enforced by the server."
              {...register("new_password")}
              value={watch("new_password")}
              error={errors.new_password?.message}
            />
            <PasswordInput
              label="Confirm new password"
              autoComplete="new-password"
              {...register("confirm_password")}
              value={watch("confirm_password")}
              error={errors.confirm_password?.message}
            />
            <Button type="submit" loading={change.isPending}>
              Update password
            </Button>
          </form>
        </CardBody>
      </Card>
    </div>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-slate-500">{label}</dt>
      <dd className={`break-all text-right ${mono ? "font-mono text-xs" : ""} text-slate-800`}>
        {value}
      </dd>
    </>
  );
}
