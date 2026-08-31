import { useMemo, useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { filesApi, websitesApi } from "../../services";
import { useAuth } from "../../features/auth/AuthContext";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import type { FSEntry } from "../../types/api";
import { Card } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Modal } from "../../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../../components/ui/States";

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function WebsiteFilesPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();

  const [cwd, setCwd] = useState("");
  const [showUpload, setShowUpload] = useState(false);
  const [showMkdir, setShowMkdir] = useState(false);
  const [renaming, setRenaming] = useState<FSEntry | null>(null);
  const [editing, setEditing] = useState<FSEntry | null>(null);

  const websiteQuery = useQuery({
    queryKey: ["websites", id],
    queryFn: () => websitesApi.get(id),
    enabled: !!id,
  });

  const listQuery = useQuery({
    queryKey: ["files", id, cwd],
    queryFn: () => filesApi.list(id, cwd),
    enabled: !!id,
  });

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["files", id, cwd] });

  const mkdirMutation = useMutation({
    mutationFn: (name: string) => filesApi.mkdir(id, join(cwd, name)),
    onSuccess: () => {
      toast.success("Directory created");
      setShowMkdir(false);
      invalidate();
    },
    onError: (e) => toast.error(errMessage(e, "Create failed")),
  });

  const removeMutation = useMutation({
    mutationFn: (entry: FSEntry) => filesApi.remove(id, entry.path),
    onSuccess: () => {
      toast.success("Deleted");
      invalidate();
    },
    onError: (e) => toast.error(errMessage(e, "Delete failed")),
  });

  const renameMutation = useMutation({
    mutationFn: ({ entry, name }: { entry: FSEntry; name: string }) => {
      const parent = entry.path.slice(0, entry.path.lastIndexOf("/"));
      return filesApi.rename(id, entry.path, join(parent, name));
    },
    onSuccess: () => {
      toast.success("Renamed");
      setRenaming(null);
      invalidate();
    },
    onError: (e) => toast.error(errMessage(e, "Rename failed")),
  });

  const canManage = hasPermission("websites.config.manage");

  if (websiteQuery.isLoading) return <Spinner label="Loading website…" />;
  if (websiteQuery.isError)
    return <ErrorState error={websiteQuery.error} onRetry={() => void websiteQuery.refetch()} />;
  if (listQuery.isLoading) return <Spinner label="Loading files…" />;
  if (listQuery.isError)
    return <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />;

  const entries = listQuery.data?.entries ?? [];
  const sitePath = cwd ? `/${cwd.split("/").filter(Boolean).join("/")}` : "/";

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <button className="mb-1 text-xs font-medium text-slate-500 hover:text-indigo-600" onClick={() => navigate(`/app/websites/${id}`)}>
            ← {websiteQuery.data?.domain}
          </button>
          <h1 className="text-xl font-semibold text-slate-900">File Manager</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            <Breadcrumb cwd={cwd} onNavigate={(p) => setCwd(p)} />
          </p>
        </div>
        {canManage && (
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => setShowMkdir(true)}>
              New folder
            </Button>
            <Button size="sm" onClick={() => setShowUpload(true)}>
              Upload
            </Button>
          </div>
        )}
      </div>

      {entries.length === 0 && cwd === "" ? (
        <EmptyState
          title="Empty document root"
          description="Upload files or create folders to start building this site."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[640px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-4 py-3 font-medium">Size</th>
                  <th className="px-4 py-3 font-medium">Modified</th>
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {cwd && (
                  <tr className="cursor-pointer text-slate-500 hover:bg-slate-50" onClick={() => setCwd(parentOf(cwd))}>
                    <td className="px-6 py-3 font-medium">.. (up)</td>
                    <td colSpan={3} />
                  </tr>
                )}
                {entries.map((e) => (
                  <FileRow
                    key={e.path}
                    entry={e}
                    canManage={canManage}
                    onOpen={() => (e.is_dir ? setCwd(relOf(e.path)) : setEditing(e))}
                    onDelete={() => {
                      if (window.confirm(`Delete ${e.name}? This cannot be undone.`)) {
                        removeMutation.mutate(e);
                      }
                    }}
                    onRename={() => setRenaming(e)}
                    removePending={removeMutation.isPending && removeMutation.variables?.path === e.path}
                  />
                ))}
              </tbody>
            </table>
          </div>
          <div className="border-t border-slate-100 px-6 py-2 text-xs text-slate-400">{sitePath}</div>
        </Card>
      )}

      {showUpload && (
        <UploadModal
          websiteId={id}
          cwd={cwd}
          onClose={() => setShowUpload(false)}
          onDone={() => {
            setShowUpload(false);
            invalidate();
          }}
        />
      )}

      {showMkdir && (
        <PromptModal
          title="New folder"
          label="Folder name"
          onSubmit={(name) => mkdirMutation.mutate(name)}
          onClose={() => setShowMkdir(false)}
          loading={mkdirMutation.isPending}
        />
      )}

      {renaming && (
        <PromptModal
          title={`Rename ${renaming.name}`}
          label="New name"
          initial={renaming.name}
          onSubmit={(name) => renameMutation.mutate({ entry: renaming, name })}
          onClose={() => setRenaming(null)}
          loading={renameMutation.isPending}
        />
      )}

      {editing && (
        <FileEditor websiteId={id} entry={editing} onClose={() => setEditing(null)} onSaved={() => invalidate()} />
      )}
    </div>
  );
}

function FileRow({
  entry,
  canManage,
  onOpen,
  onDelete,
  onRename,
  removePending,
}: {
  entry: FSEntry;
  canManage: boolean;
  onOpen: () => void;
  onDelete: () => void;
  onRename: () => void;
  removePending: boolean;
}) {
  return (
    <tr className="cursor-pointer transition-colors hover:bg-slate-50" onClick={onOpen}>
      <td className="px-6 py-2.5">
        <span className={entry.is_dir ? "font-medium text-indigo-600" : "text-slate-800"}>
          {entry.is_dir ? "📁" : "📄"} {entry.name}
        </span>
      </td>
      <td className="px-4 py-2.5 text-xs text-slate-500">{entry.is_dir ? "—" : formatBytes(entry.size)}</td>
      <td className="px-4 py-2.5 text-xs text-slate-500">
        {entry.mod_time ? new Date(entry.mod_time).toLocaleString() : "—"}
      </td>
      <td className="px-6 py-2.5 text-right">
        {canManage && (
          <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
            {!entry.is_dir && (
              <Button size="sm" variant="ghost" onClick={onOpen}>
                Edit
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={onRename}>
              Rename
            </Button>
            <Button size="sm" variant="ghost" loading={removePending} onClick={onDelete}>
              Delete
            </Button>
          </div>
        )}
      </td>
    </tr>
  );
}

function Breadcrumb({ cwd, onNavigate }: { cwd: string; onNavigate: (p: string) => void }) {
  const parts = useMemo(() => cwd.split("/").filter(Boolean), [cwd]);
  return (
    <span className="font-mono text-xs text-slate-500">
      <button className="hover:text-indigo-600" onClick={() => onNavigate("")}>
        / (root)
      </button>
      {parts.map((p, i) => (
        <span key={i}>
          {" "}/{" "}
          <button
            className="hover:text-indigo-600"
            onClick={() => onNavigate(parts.slice(0, i + 1).join("/"))}
          >
            {p}
          </button>
        </span>
      ))}
    </span>
  );
}

function join(cwd: string, name: string): string {
  if (!cwd) return name;
  return `${cwd}/${name}`;
}

function parentOf(cwd: string): string {
  const parts = cwd.split("/").filter(Boolean);
  parts.pop();
  return parts.join("/");
}

function relOf(path: string): string {
  // strip the leading slash that the agent returns for absolute paths
  return path.replace(/^\/+/, "");
}

function PromptModal({
  title,
  label,
  initial,
  onSubmit,
  onClose,
  loading,
}: {
  title: string;
  label: string;
  initial?: string;
  onSubmit: (v: string) => void;
  onClose: () => void;
  loading?: boolean;
}) {
  const [value, setValue] = useState(initial ?? "");
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4">
        <label className="mb-1.5 block text-sm font-medium text-slate-700">{label}</label>
        <input
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
          autoFocus
        />
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={loading} disabled={!value.trim()} onClick={() => onSubmit(value.trim())}>
            Save
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function UploadModal({
  websiteId,
  cwd,
  onClose,
  onDone,
}: {
  websiteId: string;
  cwd: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const [files, setFiles] = useState<FileList | null>(null);
  const [busy, setBusy] = useState(false);

  const uploadAll = async () => {
    if (!files || files.length === 0) return;
    setBusy(true);
    try {
      for (const f of Array.from(files)) {
        const data = await f.arrayBuffer();
        const b64 = btoa(String.fromCharCode(...new Uint8Array(data)));
        await filesApi.write(websiteId, join(cwd, f.name), b64);
      }
      toast.success(`Uploaded ${files.length} file(s)`);
      onDone();
    } catch (e) {
      toast.error(errMessage(e, "Upload failed"));
      setBusy(false);
    }
  };

  return (
    <Modal title="Upload files" onClose={onClose}>
      <div className="space-y-4">
        <input
          type="file"
          multiple
          onChange={(e) => setFiles(e.target.files)}
          className="block w-full text-sm text-slate-600 file:mr-3 file:rounded-md file:border-0 file:bg-indigo-50 file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-indigo-700"
        />
        <p className="text-xs text-slate-500">Each file must be under 4 MiB. Uploads go to the current folder.</p>
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={busy} disabled={!files || files.length === 0} onClick={() => void uploadAll()}>
            Upload
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function FileEditor({
  websiteId,
  entry,
  onClose,
  onSaved,
}: {
  websiteId: string;
  entry: FSEntry;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const readQuery = useQuery({
    queryKey: ["files", websiteId, "read", entry.path],
    queryFn: () => filesApi.read(websiteId, entry.path, 1 << 20),
    enabled: !!websiteId,
  });
  const [content, setContent] = useState("");
  const [saved, setSaved] = useState(false);

  const saveMutation = useMutation({
    mutationFn: async () => {
      const b64 = btoa(String.fromCharCode(...new TextEncoder().encode(content)));
      await filesApi.write(websiteId, entry.path, b64);
    },
    onSuccess: () => {
      setSaved(true);
      toast.success("Saved");
      onSaved();
    },
    onError: (e) => toast.error(errMessage(e, "Save failed")),
  });

  if (readQuery.isLoading) return <Spinner label="Reading file…" />;
  if (readQuery.isError)
    return <ErrorState error={readQuery.error} onRetry={() => void readQuery.refetch()} />;

  const decoded = atob(readQuery.data?.content_base64 ?? "");

  return (
    <Modal title={`Edit ${entry.name}`} onClose={onClose} wide>
      <textarea
        value={content === "" && !saved ? decoded : content}
        onChange={(e) => {
          setContent(e.target.value);
          setSaved(false);
        }}
        rows={18}
        spellCheck={false}
        className="w-full rounded-lg border border-slate-300 bg-slate-950 p-3 font-mono text-xs text-slate-100 focus-ring"
      />
      {readQuery.data?.truncated && (
        <p className="mt-1 text-xs text-amber-600">File was truncated for display — editing overwrites the whole file.</p>
      )}
      <div className="mt-3 flex justify-end gap-2 border-t border-slate-100 pt-3">
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
        <Button loading={saveMutation.isPending} onClick={() => saveMutation.mutate()}>
          Save
        </Button>
      </div>
    </Modal>
  );
}