import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { usersApi, rolesApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import type { UserView } from "../types/api";
import { Card, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input, PasswordInput } from "../components/ui/Input";
import { Alert } from "../components/ui/Alert";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../components/ui/States";

interface Notice {
  tone: "success" | "danger";
  text: string;
}

export function UsersPage() {
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<UserView | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);

  const listQuery = useQuery({ queryKey: ["users"], queryFn: usersApi.list });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["users"] });
    void queryClient.invalidateQueries({ queryKey: ["dashboard", "summary"] });
  };

  if (listQuery.isLoading) return <Spinner label="Loading users…" />;
  if (listQuery.isError)
    return <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />;

  const users = listQuery.data?.users ?? [];

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Users</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Panel accounts and role bindings. The last active super_admin is protected.
          </p>
        </div>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          Create user
        </Button>
      </div>

      {notice && (
        <Alert tone={notice.tone} title={notice.tone === "success" ? "Done" : undefined}>
          {notice.text}
        </Alert>
      )}

      {users.length === 0 ? (
        <EmptyState title="No users" description="Create the first managed account." />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Account</th>
                  <th className="px-4 py-3 font-medium">Roles</th>
                  <th className="px-4 py-3 font-medium">Last login</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-6 py-3 font-medium text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {users.map((u) => (
                  <tr key={u.id} className="transition-colors hover:bg-slate-50/70">
                    <td className="px-6 py-3">
                      <p className="font-medium text-slate-900">{u.display_name || u.username}</p>
                      <p className="text-xs text-slate-500">{u.email ?? u.username}</p>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex flex-wrap gap-1">
                        {u.roles.map((r) => (
                          <Badge key={r} tone={r === "super_admin" ? "info" : "neutral"}>
                            {r}
                          </Badge>
                        ))}
                        {u.roles.length === 0 && <span className="text-xs text-slate-400">—</span>}
                      </div>
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-600">
                      {u.last_login_at ? new Date(u.last_login_at).toLocaleString() : "never"}
                    </td>
                    <td className="px-4 py-3">
                      <Badge tone={u.is_active ? "success" : "danger"}>
                        {u.is_active ? "active" : "disabled"}
                      </Badge>
                    </td>
                    <td className="px-6 py-3 text-right">
                      <Button variant="outline" size="sm" onClick={() => setEditing(u)}>
                        Manage
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {createOpen && (
        <CreateUserModal
          onClose={() => setCreateOpen(false)}
          onChanged={(m) => {
            setCreateOpen(false);
            setNotice(m);
            invalidate();
          }}
        />
      )}

      {editing && (
        <EditUserModal
          user={editing}
          onClose={() => setEditing(null)}
          onChanged={(m) => {
            setEditing(null);
            setNotice(m);
            invalidate();
          }}
        />
      )}
    </div>
  );
}

function useRoleNames(): { loading: boolean; names: string[]; isError?: boolean } {
  const q = useQuery({ queryKey: ["roles", "detail"], queryFn: rolesApi.listDetail });
  return {
    loading: q.isLoading,
    isError: q.isError,
    names: (q.data?.roles ?? []).map((r) => r.name),
  };
}

function RolesChecklist({
  selected,
  onChange,
}: {
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const { loading, names } = useRoleNames();

  if (loading) return <Spinner label="Loading roles…" />;

  return (
    <fieldset>
      <legend className="mb-1.5 block text-sm font-medium text-slate-700">Roles</legend>
      <div className="grid gap-1 rounded-lg border border-slate-200 p-3 sm:grid-cols-2">
        {names.length === 0 && <p className="text-xs text-slate-400">No roles defined yet.</p>}
        {names.map((name) => (
          <label
            key={name}
            className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-slate-50"
          >
            <input
              type="checkbox"
              className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus-ring"
              checked={selected.includes(name)}
              onChange={(e) =>
                onChange(
                  e.target.checked
                    ? [...selected, name]
                    : selected.filter((x) => x !== name),
                )
              }
            />
            {name}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function CreateUserModal({
  onClose,
  onChanged,
}: {
  onClose: () => void;
  onChanged: (message: Notice) => void;
}) {
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [rolesSel, setRolesSel] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      usersApi.create({
        username,
        display_name: displayName || undefined,
        email: email || undefined,
        password,
        roles: rolesSel,
      }),
    onSuccess: (created) => onChanged({ tone: "success", text: `User "${created.username}" created.` }),
    onError: (err) => setError(err instanceof Error ? err.message : "Creation failed"),
  });

  return (
    <Modal title="Create user" onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          setError(null);
          create.mutate();
        }}
      >
        <Input label="Username" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        <Input label="Display name (optional)" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        <Input label="Email (optional)" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        <PasswordInput
          label="Password"
          showMeter
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          hint="Minimum 12 characters; policy is enforced by the server."
        />
        <RolesChecklist selected={rolesSel} onChange={setRolesSel} />
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Create
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function EditUserModal({
  user,
  onClose,
  onChanged,
}: {
  user: UserView;
  onClose: () => void;
  onChanged: (message: Notice) => void;
}) {
  const { hasPermission } = useAuth();
  const canDelete = hasPermission("users.delete");

  const [displayName, setDisplayName] = useState(user.display_name ?? "");
  const [email, setEmail] = useState(user.email ?? "");
  const [isActive, setIsActive] = useState(user.is_active);
  const [rolesSel, setRolesSel] = useState<string[]>(user.roles);
  const [newPassword, setNewPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: () =>
      usersApi.update(user.id, {
        display_name: displayName,
        email,
        is_active: isActive,
        roles: rolesSel,
        ...(newPassword ? { new_password: newPassword } : {}),
      }),
    onSuccess: () => onChanged({ tone: "success", text: `User "${user.username}" updated.` }),
    onError: (err) => setError(err instanceof Error ? err.message : "Update failed"),
  });

  const remove = useMutation({
    mutationFn: () => usersApi.delete(user.id),
    onSuccess: () =>
      onChanged({
        tone: "success",
        text: `User "${user.username}" deleted and sessions revoked.`,
      }),
    onError: (err) => setError(err instanceof Error ? err.message : "Delete failed"),
  });

  return (
    <Modal title={`Manage ${user.username}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          setError(null);
          save.mutate();
        }}
      >
        <Input label="Display name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        <PasswordInput
          label="Set new password (optional)"
          showMeter
          value={newPassword}
          onChange={(e) => setNewPassword(e.target.value)}
          hint="Leave blank to keep the current password. All of their sessions will be revoked."
        />
        <label className="flex items-center gap-2 rounded-lg border border-slate-200 px-3 py-2.5 text-sm">
          <input
            type="checkbox"
            className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus-ring"
            checked={isActive}
            onChange={(e) => setIsActive(e.target.checked)}
          />
          Account active
        </label>
        <RolesChecklist selected={rolesSel} onChange={setRolesSel} />

        {error && <Alert tone="danger">{error}</Alert>}

        <div className="flex items-center justify-between gap-3 border-t border-slate-100 pt-4">
          {canDelete ? (
            <Button
              variant="danger"
              size="sm"
              loading={remove.isPending}
              onClick={() => {
                if (
                  window.confirm(
                    `Delete ${user.username} permanently? This also revokes all of their sessions.`,
                  )
                )
                  remove.mutate();
              }}
            >
              Delete account
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              Save changes
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
}
