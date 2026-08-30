import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { rolesApi } from "../services";
import type { PermissionView, RoleView } from "../types/api";
import { Card, CardBody, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { Alert } from "../components/ui/Alert";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../components/ui/States";

interface Notice {
  tone: "success" | "danger";
  text: string;
}

export function RolesPage() {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<RoleView | null>(null);
  const [creating, setCreating] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);

  const rolesQuery = useQuery({ queryKey: ["roles", "detail"], queryFn: rolesApi.listDetail });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => rolesApi.deleteRole(id),
    onSuccess: () => {
      setNotice({ tone: "success", text: "Role deleted." });
      void queryClient.invalidateQueries({ queryKey: ["roles"] });
    },
    onError: (err) => setNotice({ tone: "danger", text: err instanceof Error ? err.message : "" }),
  });

  if (rolesQuery.isLoading) return <Spinner label="Loading roles…" />;
  if (rolesQuery.isError)
    return <ErrorState error={rolesQuery.error} onRetry={() => void rolesQuery.refetch()} />;

  const roles = rolesQuery.data?.roles ?? [];
  const invalidate = () => {
    setEditing(null);
    setCreating(false);
    void queryClient.invalidateQueries({ queryKey: ["roles"] });
  };

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Roles</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            System roles form the platform baseline and cannot be modified. Custom roles are fully
            editable.
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          Create role
        </Button>
      </div>

      {notice && (
        <Alert tone={notice.tone} title={notice.tone === "success" ? "Done" : undefined}>
          {notice.text}
        </Alert>
      )}

      {roles.length === 0 ? (
        <EmptyState title="No roles defined" />
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {roles.map((role) => (
            <Card key={role.id}>
              <CardBody>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="flex items-center gap-2">
                      <h2 className="font-semibold text-slate-900">{role.name}</h2>
                      {role.is_system ? (
                        <Badge tone="info">system</Badge>
                      ) : (
                        <Badge tone="neutral">custom</Badge>
                      )}
                    </div>
                    <p className="mt-1 min-h-[1rem] text-xs text-slate-500">{role.description}</p>
                  </div>
                  <span className="shrink-0 text-xs text-slate-400">
                    {role.user_count} user{role.user_count === 1 ? "" : "s"}
                  </span>
                </div>

                <div className="mt-4 flex flex-wrap gap-1.5">
                  {role.permissions.map((p) => (
                    <code
                      key={p}
                      title={p}
                      className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600"
                    >
                      {p}
                    </code>
                  ))}
                  {role.permissions.length === 0 && (
                    <span className="text-xs italic text-slate-400">no permissions granted</span>
                  )}
                </div>

                {!role.is_system && (
                  <div className="mt-4 flex gap-2 border-t border-slate-100 pt-3">
                    <Button variant="outline" size="sm" onClick={() => setEditing(role)}>
                      Edit permissions
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      loading={deleteMutation.isPending}
                      onClick={() => {
                        if (window.confirm(`Delete role "${role.name}"? Members will lose these permissions.`))
                          deleteMutation.mutate(role.id);
                      }}
                    >
                      Delete
                    </Button>
                  </div>
                )}
              </CardBody>
            </Card>
          ))}
        </div>
      )}

      {(creating || editing) && (
        <RoleEditorModal
          existing={editing ?? undefined}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={(m) => {
            setNotice(m);
            invalidate();
          }}
        />
      )}
    </div>
  );
}

function groupPermissions(perms: PermissionView[]): Record<string, PermissionView[]> {
  const groups: Record<string, PermissionView[]> = {};
  for (const p of perms) {
    const key = p.code.split(".")[0];
    (groups[key] ??= []).push(p);
  }
  return groups;
}

function RoleEditorModal({
  existing,
  onClose,
  onSaved,
}: {
  existing?: RoleView;
  onClose: () => void;
  onSaved: (message: Notice) => void;
}) {
  const [name, setName] = useState(existing?.name ?? "");
  const [description, setDescription] = useState(existing?.description ?? "");
  const [selected, setSelected] = useState<string[]>(existing?.permissions ?? []);
  const [error, setError] = useState<string | null>(null);

  const permsQuery = useQuery({ queryKey: ["permissions"], queryFn: rolesApi.permissions });

  const save = useMutation({
    mutationFn: () =>
      existing
        ? rolesApi.update(existing.id, { description, permissions: selected })
        : rolesApi.create({ name, description, permissions: selected }),
    onSuccess: (r) =>
      onSaved({ tone: "success", text: `Role "${r.name}" ${existing ? "updated" : "created"}.` }),
    onError: (err) => setError(err instanceof Error ? err.message : "Save failed"),
  });

  const groups = useMemo(() => groupPermissions(permsQuery.data?.permissions ?? []), [permsQuery.data]);
  const toggle = (code: string) =>
    setSelected((prev) => (prev.includes(code) ? prev.filter((c) => c !== code) : [...prev, code]));

  return (
    <Modal wide title={existing ? `Edit role: ${existing.name}` : "Create role"} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          setError(null);
          save.mutate();
        }}
      >
        {!existing && (
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} autoFocus hint="Letters, digits, dash or underscore." />
        )}
        <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />

        {permsQuery.isLoading && <Spinner label="Loading permissions…" />}
        {permsQuery.isError && (
          <Alert tone="danger">Could not load the permission catalogue.</Alert>
        )}

        <fieldset className="space-y-4">
          <legend className="mb-1 block text-sm font-medium text-slate-700">
            Permissions ({selected.length} selected)
          </legend>
          {Object.entries(groups).map(([group, perms]) => (
            <div key={group} className="rounded-lg border border-slate-200 p-3">
              <p className="pb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
                {group}
              </p>
              <div className="grid gap-1 sm:grid-cols-2">
                {perms.map((p) => (
                  <label
                    key={p.code}
                    className="flex cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-slate-50"
                  >
                    <input
                      type="checkbox"
                      className="mt-0.5 h-4 w-4 rounded border-slate-300 text-indigo-600 focus-ring"
                      checked={selected.includes(p.code)}
                      onChange={() => toggle(p.code)}
                    />
                    <span>
                      <code className="font-mono text-xs text-slate-800">{p.code}</code>
                      <span className="block text-[11px] leading-snug text-slate-500">
                        {p.description}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
            </div>
          ))}
        </fieldset>

        {error && <Alert tone="danger">{error}</Alert>}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={save.isPending}>
            {existing ? "Save changes" : "Create role"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
