import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { databasesApi } from "../../services";
import { useAuth } from "../../features/auth/AuthContext";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import { DatabaseStatusBadge } from "../DatabasesPage";
import { Card, CardBody, CardHeader, Badge } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Modal } from "../../components/ui/Modal";
import { Spinner, ErrorState } from "../../components/ui/States";
import { Alert } from "../../components/ui/Alert";

const USER_NAME_RE = /^[a-z][a-z0-9_]{0,31}$/;

export function DatabaseDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();

  const dbQuery = useQuery({
    queryKey: ["databases", id],
    queryFn: () => databasesApi.get(id),
    enabled: !!id,
    refetchInterval: (q) => (q.state.data?.status === "provisioning" ? 3000 : false),
  });

  const [showAddUser, setShowAddUser] = useState(false);
  const [newPassword, setNewPassword] = useState<{ username: string; password: string } | null>(null);

  const deleteDb = useMutation({
    mutationFn: () => databasesApi.delete(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["databases"] });
      toast.success("Database deletion started");
      navigate("/app/databases");
    },
    onError: (e) => toast.error(errMessage(e, "Delete failed")),
  });

  const rotate = useMutation({
    mutationFn: (userId: string) => databasesApi.rotatePassword(id, userId),
    onSuccess: (res, userId) => {
      const u = dbQuery.data?.users.find((x) => x.id === userId);
      setNewPassword({ username: u?.username ?? "user", password: res.password });
    },
    onError: (e) => toast.error(errMessage(e, "Rotation failed")),
  });

  const dropUser = useMutation({
    mutationFn: (userId: string) => databasesApi.deleteUser(id, userId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["databases", id] });
      toast.success("User removed");
    },
    onError: (e) => toast.error(errMessage(e, "Remove user failed")),
  });

  if (dbQuery.isLoading) return <Spinner label="Loading database…" />;
  if (dbQuery.isError) return <ErrorState error={dbQuery.error} onRetry={() => void dbQuery.refetch()} />;

  const d = dbQuery.data!;
  const port = d.engine === "mysql" ? 3306 : 5432;

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold text-slate-900">{d.name}</h1>
            <Badge tone="neutral">{d.engine}</Badge>
            <DatabaseStatusBadge status={d.status} />
          </div>
          <p className="mt-1 text-sm text-slate-500">on {d.server_name}</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => navigate("/app/databases")}>
          All databases
        </Button>
      </div>

      {d.status === "error" && d.error && (
        <Alert tone="danger" title="Provisioning failed">{d.error}</Alert>
      )}

      <Card>
        <CardHeader title="Connection details" />
        <CardBody>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
            <dt className="text-slate-500">Database name</dt>
            <dd className="text-right font-mono text-xs text-slate-800">{d.name}</dd>
            <dt className="text-slate-500">Engine</dt>
            <dd className="text-right font-medium text-slate-800">{d.engine}</dd>
            <dt className="text-slate-500">Host</dt>
            <dd className="text-right font-mono text-xs text-slate-800">127.0.0.1</dd>
            <dt className="text-slate-500">Port</dt>
            <dd className="text-right font-mono text-xs text-slate-800">{port}</dd>
          </dl>
          <p className="mt-3 text-xs text-slate-400">
            Applications on this server connect over localhost. Create a user below to get credentials.
          </p>
        </CardBody>
      </Card>

      <Card>
        <CardHeader
          title="Users"
          actions={
            hasPermission("databases.users.manage") && d.status === "active" ? (
              <Button size="sm" onClick={() => setShowAddUser(true)}>Add user</Button>
            ) : undefined
          }
        />
        <CardBody>
          {d.users.length === 0 ? (
            <p className="text-sm text-slate-500">No users yet.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                    <th className="py-2 font-medium">Username</th>
                    <th className="py-2 font-medium">Status</th>
                    <th className="py-2 text-right font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-50">
                  {d.users.map((u) => (
                    <tr key={u.id}>
                      <td className="py-2.5 font-mono text-xs text-slate-800">{u.username}</td>
                      <td className="py-2.5"><Badge tone={u.status === "active" ? "success" : "neutral"}>{u.status}</Badge></td>
                      <td className="py-2.5 text-right">
                        {hasPermission("databases.users.manage") && (
                          <div className="flex justify-end gap-2">
                            <Button size="sm" variant="outline" loading={rotate.isPending && rotate.variables === u.id} onClick={() => rotate.mutate(u.id)}>
                              Reset password
                            </Button>
                            <Button size="sm" variant="ghost" loading={dropUser.isPending && dropUser.variables === u.id} onClick={() => {
                              if (window.confirm(`Remove user ${u.username}?`)) dropUser.mutate(u.id);
                            }}>
                              Remove
                            </Button>
                          </div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardBody>
      </Card>

      {hasPermission("databases.delete") && (
        <Card className="border-red-100">
          <CardHeader title="Danger zone" />
          <CardBody className="flex items-center justify-between gap-4">
            <p className="text-sm text-slate-600">
              Deleting <span className="font-medium text-slate-800">{d.name}</span> drops the database and all its users on the server.
            </p>
            <Button variant="danger" loading={deleteDb.isPending} onClick={() => {
              if (window.confirm(`Delete database ${d.name}? This cannot be undone.`)) deleteDb.mutate();
            }}>
              Delete database
            </Button>
          </CardBody>
        </Card>
      )}

      {showAddUser && (
        <AddUserModal
          databaseId={id}
          onClose={() => setShowAddUser(false)}
          onCreated={(username, password) => {
            setShowAddUser(false);
            void queryClient.invalidateQueries({ queryKey: ["databases", id] });
            setNewPassword({ username, password });
          }}
        />
      )}

      {newPassword && (
        <PasswordRevealModal username={newPassword.username} password={newPassword.password} onClose={() => setNewPassword(null)} />
      )}
    </div>
  );
}

function AddUserModal({
  databaseId,
  onClose,
  onCreated,
}: {
  databaseId: string;
  onClose: () => void;
  onCreated: (username: string, password: string) => void;
}) {
  const toast = useToast();
  const [username, setUsername] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => databasesApi.createUser(databaseId, username.trim()),
    onSuccess: (res) => onCreated(res.user.username, res.password),
    onError: (e) => toast.error(errMessage(e, "Create user failed")),
  });

  const submit = () => {
    if (!USER_NAME_RE.test(username.trim())) {
      setError("Lowercase letters, digits or underscore; start with a letter (max 32).");
      return;
    }
    setError(null);
    create.mutate();
  };

  return (
    <Modal title="Add database user" onClose={onClose}>
      <div className="space-y-4">
        <Input
          label="Username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          error={error ?? undefined}
          hint="A strong password is generated and shown once."
          autoComplete="off"
        />
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={create.isPending} onClick={submit}>Create user</Button>
        </div>
      </div>
    </Modal>
  );
}

function PasswordRevealModal({
  username,
  password,
  onClose,
}: {
  username: string;
  password: string;
  onClose: () => void;
}) {
  return (
    <Modal title="User credentials" onClose={onClose}>
      <div className="space-y-4">
        <Alert tone="warning" title="Copy this password now">
          It will not be shown again. EpicPanel never stores database passwords.
        </Alert>
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-500">Username</p>
          <code className="mt-1 block rounded-lg bg-slate-900 px-3 py-2 text-sm text-slate-100">{username}</code>
        </div>
        <div>
          <p className="text-xs uppercase tracking-wide text-slate-500">Password</p>
          <code className="mt-1 block break-all rounded-lg bg-slate-900 px-3 py-2 text-sm text-emerald-300">{password}</code>
        </div>
        <div className="flex justify-end border-t border-slate-100 pt-4">
          <Button onClick={onClose}>Done</Button>
        </div>
      </div>
    </Modal>
  );
}
