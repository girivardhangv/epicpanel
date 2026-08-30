import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { domainsApi, jobsApi, serversApi, websitesApi } from "../../services";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import {
  buildProvisionRequest,
  eligibleAliasDomains,
  eligiblePrimaryDomains,
  isStepComplete,
  siteDocumentRoot,
  sortedPHPVersions,
  usableServers,
  WIZARD_STEPS,
  type WizardData,
} from "./wizardLogic";
import type { JobView } from "../../types/api";
import { Card, CardBody } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Spinner, ErrorState, EmptyState } from "../../components/ui/States";
import { Alert } from "../../components/ui/Alert";
import { CheckCircle2, XCircle } from "lucide-react";

export function CreateWebsiteWizardPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  const [step, setStep] = useState(0);
  const [data, setData] = useState<WizardData>({
    serverId: "",
    domainId: "",
    phpVersion: "",
    aliasIds: [],
  });

  const serversQuery = useQuery({ queryKey: ["servers"], queryFn: () => serversApi.list() });
  const domainsQuery = useQuery({ queryKey: ["domains"], queryFn: () => domainsApi.list() });
  const servers = usableServers(serversQuery.data?.servers ?? []);
  const activeServer = useMemo(
    () => servers.find((s) => s.id === data.serverId) ?? null,
    [servers, data.serverId],
  );

  const phpQuery = useQuery({
    queryKey: ["servers", data.serverId, "php-versions"],
    queryFn: () => serversApi.phpVersions(data.serverId),
    enabled: !!data.serverId,
  });
  const phpVersions = sortedPHPVersions(phpQuery.data?.versions ?? []);

  const update = (patch: Partial<WizardData>) => setData((d) => ({ ...d, ...patch }));

  // ---- provisioning --------------------------------------------------------
  const [job, setJob] = useState<JobView | null>(null);
  const [jobError, setJobError] = useState<string | null>(null);

  const createMutation = useMutation({
    mutationFn: () => websitesApi.create(buildProvisionRequest(data)),
    onSuccess: (res) => {
      setJob(res.job);
      setStep(4);
      void queryClient.invalidateQueries({ queryKey: ["websites"] });
      void queryClient.invalidateQueries({ queryKey: ["domains"] });
    },
    onError: (err) => toast.error(errMessage(err, "Website creation failed")),
  });

  // Poll the job while it is queued/running; the cache keeps the latest state.
  const jobPollQuery = useQuery({
    queryKey: ["jobs", job?.id],
    queryFn: () => jobsApi.get(job!.id),
    enabled: !!job && (job.status === "queued" || job.status === "running"),
    refetchInterval: 1500,
  });

  const currentJob: JobView | undefined = jobPollQuery.data ?? job ?? undefined;
  const jobFailed = currentJob?.status === "failed";
  const jobDone = currentJob?.status === "completed";

  useEffect(() => {
    if (jobDone && step === 4) setStep(5);
  }, [jobDone, step]);

  useEffect(() => {
    if (jobFailed && currentJob) {
      setJobError(currentJob.error || "Provisioning failed");
    }
  }, [jobFailed, currentJob]);

  // ---- guards --------------------------------------------------------------
  if (serversQuery.isLoading || domainsQuery.isLoading) {
    return <Spinner label="Preparing the wizard…" />;
  }
  if (serversQuery.isError)
    return <ErrorState error={serversQuery.error} onRetry={() => void serversQuery.refetch()} />;
  if (domainsQuery.isError)
    return <ErrorState error={domainsQuery.error} onRetry={() => void domainsQuery.refetch()} />;

  const primaryDomains = data.serverId
    ? eligiblePrimaryDomains(domainsQuery.data?.domains ?? [], data.serverId)
    : [];
  const aliasDomains = data.serverId
    ? eligibleAliasDomains(domainsQuery.data?.domains ?? [], data.serverId)
    : [];
  const selectedDomain = primaryDomains.find((d) => d.id === data.domainId) ?? null;

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Create website</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Provision a website with Nginx and PHP on one of your servers.
        </p>
      </div>

      <ol className="flex flex-wrap gap-2" aria-label="Wizard progress">
        {WIZARD_STEPS.map((label, i) => (
          <li
            key={label}
            className={[
              "flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium",
              i === step
                ? "bg-indigo-600 text-white"
                : i < step
                  ? "bg-indigo-50 text-indigo-700"
                  : "bg-slate-100 text-slate-500",
            ].join(" ")}
          >
            <span aria-hidden>{i < step ? "✓" : `0${i + 1}`}</span>
            {label}
          </li>
        ))}
      </ol>

      <Card>
        <CardBody className="space-y-5">
          {step === 0 && (
            <StepDomain
              servers={servers}
              serverId={data.serverId}
              domainId={data.domainId}
              primaryDomains={primaryDomains}
              onServer={(id) => update({ serverId: id, domainId: "", aliasIds: [] })}
              onDomain={(id) => update({ domainId: id })}
              onAliases={(ids) => update({ aliasIds: ids })}
              aliasDomains={aliasDomains}
              aliasIds={data.aliasIds}
            />
          )}

          {step === 1 && (
            <StepRuntime
              phpVersions={phpVersions}
              phpLoading={phpQuery.isLoading}
              phpSource={phpQuery.data?.source}
              selected={data.phpVersion}
              onSelect={(v) => update({ phpVersion: v })}
              serverReachable={activeServer?.manageable ?? false}
            />
          )}

          {step === 2 && selectedDomain && (
            <StepStorage
              os={activeServer?.os ?? "linux"}
              domain={selectedDomain.domain}
            />
          )}

          {step === 3 && activeServer && selectedDomain && (
            <StepReview
              data={data}
              serverName={activeServer.label || activeServer.hostname}
              serverOs={activeServer.os}
              domain={selectedDomain.domain}
              aliasNames={aliasDomains
                .filter((d) => data.aliasIds.includes(d.id))
                .map((d) => d.domain)}
              docRoot={siteDocumentRoot(activeServer.os, selectedDomain.domain)}
            />
          )}

          {step === 4 && currentJob && <StepProvision job={currentJob} />}
          {step === 4 && !currentJob && <Spinner label="Queuing provisioning job…" />}
          {step === 4 && jobError && !currentJob && <p role="alert">{jobError}</p>}

          {step === 5 && selectedDomain && (
            <div className="space-y-4 text-center">
              <CheckCircle2 className="mx-auto h-12 w-12 text-emerald-500" aria-hidden />
              <div>
                <p className="text-base font-semibold text-slate-900">
                  {selectedDomain.domain} is live
                </p>
                <p className="mt-1 text-sm text-slate-500">
                  The default page is in place — visit the site or open it in the manager.
                </p>
              </div>
              <div className="flex justify-center gap-2">
                <a
                  href={`http://${selectedDomain.domain}`}
                  target="_blank"
                  rel="noreferrer"
                  className="focus-ring inline-flex h-10 items-center rounded-lg border border-slate-300 bg-white px-4 text-sm font-medium text-slate-700 shadow-sm hover:bg-slate-50"
                >
                  Visit website
                </a>
                {currentJob?.website_id && (
                  <Button onClick={() => navigate(`/app/websites/${currentJob.website_id}`)}>
                    Manage website
                  </Button>
                )}
              </div>
            </div>
          )}
        </CardBody>
      </Card>

      {step < 4 && (
        <div className="flex justify-between">
          <Button variant="ghost" disabled={step === 0} onClick={() => setStep((s) => s - 1)}>
            Back
          </Button>
          {step < 3 ? (
            <Button
              disabled={!isStepComplete(step, data)}
              onClick={() => setStep((s) => s + 1)}
            >
              Continue
            </Button>
          ) : (
            <Button loading={createMutation.isPending} onClick={() => createMutation.mutate()}>
              Provision website
            </Button>
          )}
        </div>
      )}

      {step === 4 && jobFailed && currentJob?.website_id && (
        <div className="flex justify-between">
          <Button variant="ghost" onClick={() => navigate("/app/websites")}>
            Back to websites
          </Button>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------

function StepDomain({
  servers,
  serverId,
  domainId,
  primaryDomains,
  aliasDomains,
  aliasIds,
  onServer,
  onDomain,
  onAliases,
}: {
  servers: { id: string; label: string; hostname: string; os: string; manageable: boolean }[];
  serverId: string;
  domainId: string;
  primaryDomains: { id: string; domain: string }[];
  aliasDomains: { id: string; domain: string }[];
  aliasIds: string[];
  onServer: (id: string) => void;
  onDomain: (id: string) => void;
  onAliases: (ids: string[]) => void;
}) {
  if (servers.length === 0) {
    return (
      <EmptyState
        title="No manageable servers"
        description="Enroll an agent with the management channel first (re-enroll agents created before this feature)."
      />
    );
  }
  return (
    <div className="space-y-5">
      <div>
        <label htmlFor="wiz-server" className="mb-1.5 block text-sm font-medium text-slate-700">
          Server
        </label>
        <select
          id="wiz-server"
          value={serverId}
          onChange={(e) => onServer(e.target.value)}
          className="block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
        >
          <option value="">Select a server…</option>
          {servers.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label || s.hostname} ({s.os})
            </option>
          ))}
        </select>
      </div>

      {serverId && primaryDomains.length === 0 && (
        <Alert tone="warning" title="No free domains on this server">
          Add a domain first — it will be available here immediately.
        </Alert>
      )}

      {serverId && primaryDomains.length > 0 && (
        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">Domain</p>
          <div className="grid gap-2 sm:grid-cols-2">
            {primaryDomains.map((d) => (
              <button
                key={d.id}
                type="button"
                onClick={() => onDomain(d.id)}
                className={[
                  "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
                  domainId === d.id
                    ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                    : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
                ].join(" ")}
              >
                {d.domain}
              </button>
            ))}
          </div>
        </div>
      )}

      {serverId && aliasDomains.length > 0 && (
        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">
            Aliases <span className="font-normal text-slate-500">(optional)</span>
          </p>
          <div className="flex flex-wrap gap-2">
            {aliasDomains.map((d) => {
              const checked = aliasIds.includes(d.id);
              return (
                <button
                  key={d.id}
                  type="button"
                  aria-pressed={checked}
                  onClick={() =>
                    onAliases(checked ? aliasIds.filter((x) => x !== d.id) : [...aliasIds, d.id])
                  }
                  className={[
                    "rounded-full border px-3 py-1 text-xs transition-colors focus-ring",
                    checked
                      ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                      : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                  ].join(" ")}
                >
                  {d.domain}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

function StepRuntime({
  phpVersions,
  phpLoading,
  phpSource,
  selected,
  onSelect,
  serverReachable,
}: {
  phpVersions: { version: string; handler_type: string; status: string }[];
  phpLoading: boolean;
  phpSource?: string;
  selected: string;
  onSelect: (v: string) => void;
  serverReachable: boolean;
}) {
  return (
    <div className="space-y-5">
      <div>
        <p className="mb-1.5 text-sm font-medium text-slate-700">Web server</p>
        <label className="flex items-center gap-2 rounded-lg border border-indigo-600 bg-indigo-50 px-3 py-2.5 text-sm font-medium text-indigo-700">
          <input type="radio" checked readOnly className="accent-indigo-600" />
          Nginx
        </label>
        <p className="mt-1 text-xs text-slate-500">
          Apache and IIS ship in later phases behind the same interface.
        </p>
      </div>

      <div>
        <p className="mb-1.5 text-sm font-medium text-slate-700">
          PHP version{" "}
          {phpSource === "cached" && (
            <span className="font-normal text-amber-600">(cached — agent offline)</span>
          )}
        </p>
        {phpLoading ? (
          <Spinner label="Discovering PHP runtimes…" />
        ) : phpVersions.length === 0 ? (
          <Alert tone="warning" title="PHP is not installed on this server.">
            You can still create a static website, or install PHP on the server and refresh.
          </Alert>
        ) : (
          <div className="grid gap-2 sm:grid-cols-3">
            {phpVersions.map((p) => (
              <button
                key={p.version}
                type="button"
                onClick={() => onSelect(p.version === selected ? "" : p.version)}
                className={[
                  "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
                  selected === p.version
                    ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                    : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
                ].join(" ")}
              >
                <span className="block font-medium">PHP {p.version}</span>
                <span className="text-xs text-slate-500">{p.handler_type}</span>
              </button>
            ))}
            <button
              type="button"
              onClick={() => onSelect("")}
              className={[
                "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
                selected === ""
                  ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                  : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
              ].join(" ")}
            >
              <span className="block font-medium">No PHP</span>
              <span className="text-xs text-slate-500">static files</span>
            </button>
          </div>
        )}
        {!serverReachable && (
          <p className="mt-2 text-xs text-slate-500">
            Runtime discovery requires a reachable agent management channel.
          </p>
        )}
      </div>
    </div>
  );
}

function StepStorage({ os, domain }: { os: string; domain: string }) {
  const docRoot = siteDocumentRoot(os, domain);
  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-slate-200 bg-slate-50 px-4 py-3">
        <p className="text-xs uppercase tracking-wide text-slate-500">Document root (fixed convention)</p>
        <p className="mt-1 break-all font-mono text-sm text-slate-800">{docRoot}</p>
        <p className="mt-1.5 text-xs text-slate-500">
          The directory is created automatically from the domain — paths are not user-editable.
        </p>
      </div>
      <div className="rounded-lg bg-slate-50 px-4 py-3 text-xs leading-relaxed text-slate-600">
        <p className="font-semibold text-slate-700">Planned layout</p>
        <pre className="mt-1 whitespace-pre-wrap">
          {docRoot}
          {"\n"}
          {join(os, parentOf(docRoot), "logs")}
          {"  (access + error logs)\n"}
          {join(os, parentOf(docRoot), "tmp")}
          {"\n"}
          {join(os, parentOf(docRoot), "private")}
        </pre>
      </div>
    </div>
  );
}

function parentOf(p: string): string {
  const norm = p.replace(/[\\/]+$/, "");
  const i = Math.max(norm.lastIndexOf("/"), norm.lastIndexOf("\\"));
  return i > 0 ? norm.slice(0, i) : norm;
}

function join(os: string, a: string, b: string): string {
  return a + (os === "windows" ? "\\" : "/") + b;
}

function StepReview({
  data,
  serverName,
  serverOs,
  domain,
  aliasNames,
  docRoot,
}: {
  data: WizardData;
  serverName: string;
  serverOs: string;
  domain: string;
  aliasNames: string[];
  docRoot: string;
}) {
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-3 text-sm sm:grid-cols-2">
      <ReviewRow label="Domain" value={domain} />
      <ReviewRow label="Server" value={`${serverName} (${serverOs})`} />
      <ReviewRow label="Web server" value="Nginx" />
      <ReviewRow label="PHP" value={data.phpVersion ? `PHP ${data.phpVersion}` : "static (no PHP)"} />
      <ReviewRow label="Document root" value={docRoot} mono />
      <ReviewRow
        label="Aliases"
        value={aliasNames.length > 0 ? aliasNames.join(", ") : "none"}
      />
    </dl>
  );
}

function ReviewRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-slate-500">{label}</dt>
      <dd className={mono ? "break-all font-mono text-xs text-slate-800" : "font-medium text-slate-800"}>
        {value}
      </dd>
    </>
  );
}

function StepProvision({ job }: { job: JobView }) {
  const failed = job.status === "failed";
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100">
          <div
            className={[
              "h-full rounded-full transition-all duration-500",
              failed ? "bg-red-500" : "bg-indigo-600",
            ].join(" ")}
            style={{ width: `${Math.max(job.progress, 4)}%` }}
          />
        </div>
        <span className="w-12 text-right text-xs font-medium text-slate-600">{job.progress}%</span>
      </div>

      <p className="text-sm text-slate-700">{job.message || "Waiting…"}</p>

      {failed && (
        <Alert tone="danger" title="Provisioning failed">
          {job.error || "An error occurred while provisioning the website."}
        </Alert>
      )}

      <ul className="space-y-1.5 text-xs text-slate-500">
        <StepLine done={job.progress >= 15 || job.status === "completed"} label="Validating domain & plan" />
        <StepLine done={job.progress >= 30 || job.status === "completed"} label="Creating website directories" />
        <StepLine done={job.progress >= 45 || job.status === "completed"} label="Configuring PHP runtime" />
        <StepLine done={job.progress >= 60 || job.status === "completed"} label="Creating Nginx configuration" />
        <StepLine done={job.progress >= 75 || job.status === "completed"} label="Validating & reloading Nginx" />
        <StepLine done={job.progress >= 88 || job.status === "completed"} label="Creating default page" />
        <StepLine done={job.status === "completed"} label="Website activated" />
      </ul>

      {failed && job.website_id && (
        <p className="text-xs text-slate-500">
          You can safely retry from the website manager — completed steps are not repeated
          destructively.{" "}
          <Link to={`/app/websites/${job.website_id}`} className="text-indigo-600 hover:underline">
            Open website
          </Link>
        </p>
      )}
    </div>
  );
}

function StepLine({ done, label }: { done: boolean; label: string }) {
  return (
    <li className="flex items-center gap-2">
      {done ? (
        <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" aria-hidden />
      ) : (
        <XCircle className="h-3.5 w-3.5 text-slate-300" aria-hidden />
      )}
      <span className={done ? "text-slate-700" : ""}>{label}</span>
    </li>
  );
}
