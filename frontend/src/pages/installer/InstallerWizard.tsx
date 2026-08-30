import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useForm } from "react-hook-form";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  ShieldCheck,
  TerminalSquare,
} from "lucide-react";
import { installerApi } from "../../services";
import type { InstallerStatus, RequirementsReport } from "../../types/api";
import { Card, CardBody, Badge } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Input, PasswordInput } from "../../components/ui/Input";
import { Alert } from "../../components/ui/Alert";
import { Spinner, ErrorState, EmptyState } from "../../components/ui/States";

/**
 * First-run installation wizard.
 *
 * Ordering shown here is UX convenience only: every POST re-validates the
 * prerequisite state server-side and the whole surface locks permanently once
 * installation completes.
 */

const STEP_ORDER = [
  "requirements",
  "license",
  "database",
  "configuration",
  "administrator",
  "security",
  "finish",
] as const;

type StepId = (typeof STEP_ORDER)[number];

const STEPS: Array<{ id: StepId; title: string }> = [
  { id: "requirements", title: "Requirements" },
  { id: "license", title: "License" },
  { id: "database", title: "Database" },
  { id: "configuration", title: "Configuration" },
  { id: "administrator", title: "Administrator" },
  { id: "security", title: "Security" },
  { id: "finish", title: "Complete" },
];

interface Ctx {
  status: InstallerStatus;
  goTo: (step: StepId) => void;
}

export function InstallerWizardPage() {
  const navigate = useNavigate();
  const [current, setCurrent] = useState<StepId | null>(null);

  const statusQuery = useQuery({
    queryKey: ["system", "installer-status"],
    queryFn: () => installerApi.status(),
    staleTime: 0,
    refetchOnMount: true,
  });

  // If already installed, immediately hand over to the login screen.
  useEffect(() => {
    if (statusQuery.data?.installed) navigate("/login", { replace: true });
  }, [statusQuery.data?.installed, navigate]);

  const firstIncomplete = useMemo<StepId | null>(() => {
    const steps = statusQuery.data?.steps;
    if (!steps) return null;
    for (const s of STEP_ORDER.slice(0, 6)) {
      if (!steps[s]) return s;
    }
    return "finish";
  }, [statusQuery.data]);

  // Derived during render (no effect needed): an explicit user choice wins;
  // before that, the first incomplete step reported by the server is shown.
  const active: StepId | null = current ?? firstIncomplete;

  if (statusQuery.isLoading || !statusQuery.data || !active) {
    return <Shell><Spinner label="Checking installation state…" /></Shell>;
  }
  if (statusQuery.isError) {
    return (
      <Shell>
        <ErrorState error={statusQuery.error} onRetry={() => void statusQuery.refetch()} />
      </Shell>
    );
  }

  const ctx: Ctx = { status: statusQuery.data, goTo: setCurrent };

  const index = STEPS.findIndex((s) => s.id === active);
  const stepBody = {
    requirements: <StepRequirements ctx={ctx} />,
    license: <StepLicense ctx={ctx} />,
    database: <StepDatabase ctx={ctx} />,
    configuration: <StepConfiguration ctx={ctx} />,
    administrator: <StepAdministrator ctx={ctx} />,
    security: <StepSecurity ctx={ctx} />,
    finish: <StepFinish ctx={ctx} />,
  }[active];

  return (
    <Shell wide>
      <ol className="mb-8 flex flex-wrap items-center gap-x-1 gap-y-2 text-xs">
        {STEPS.map((s, i) => {
          const done = active !== s.id && (statusQuery.data!.steps[s.id] === true || i < index);
          const isCurrent = s.id === active;
          return (
            <li key={s.id} className="flex items-center gap-1">
              <span
                aria-current={isCurrent ? "step" : undefined}
                className={[
                  "inline-flex items-center gap-1.5 rounded-full px-3 py-1 font-medium",
                  isCurrent
                    ? "bg-slate-900 text-white"
                    : done
                      ? "bg-emerald-50 text-emerald-700"
                      : "bg-white text-slate-400 ring-1 ring-inset ring-slate-200",
                ].join(" ")}
              >
                {done && !isCurrent ? (
                  <Check className="h-3 w-3" aria-hidden />
                ) : (
                  <span>{String(i + 1).padStart(2, "0")}</span>
                )}
                {s.title}
              </span>
              {i < STEPS.length - 1 && <ChevronRight className="h-3 w-3 text-slate-300" />}
            </li>
          );
        })}
      </ol>

      {stepBody}

      <div className="mt-10 flex justify-between border-t border-slate-100 pt-5">
        <Button
          variant="ghost"
          disabled={index === 0}
          onClick={() => index > 0 && setCurrent(STEPS[index - 1].id)}
        >
          <ChevronLeft className="h-4 w-4" /> Back
        </Button>
        {active !== "finish" && statusQuery.data.steps[active] === true && (
          <Button variant="secondary" onClick={() => setCurrent(STEPS[Math.min(index + 1, 6)].id)}>
            Continue <ChevronRight className="h-4 w-4" />
          </Button>
        )}
      </div>
    </Shell>
  );
}

// --- shared wizard helpers ---------------------------------------------------

function Shell({ wide, children }: { wide?: boolean; children: React.ReactNode }) {
  return (
    <div className="min-h-screen bg-gradient-to-b from-slate-950 to-slate-900 px-4 py-12">
      <div className={`mx-auto ${wide ? "max-w-4xl" : "max-w-xl"} space-y-6`}>
        <div className="text-center">
          <p className="text-sm font-semibold uppercase tracking-[0.3em] text-indigo-400">EpicPanel</p>
          <h1 className="mt-2 text-2xl font-bold text-white">Installation</h1>
        </div>
        {children}
      </div>
    </div>
  );
}

// --- step bodies below share the Ctx contract --------------------------------

/* ------------------------------- steps ---------------------------------- */

function StepRequirements({ ctx }: { ctx: Ctx }) {
  const runCheck = useMutation({
    mutationFn: () => installerApi.requirements(),
    onSuccess: () => {
      void ctx;
      // Returning to requirements re-runs live probes each time.
      ctx.goTo("license");
    },
  });

  if (runCheck.isPending) return <Spinner label="Inspecting host…" />;

  if (runCheck.data) return <RequirementsReportView report={runCheck.data} />;

  return (
    <Panel title="System requirements">
      <EmptyState
        title="Not yet inspected"
        description="The installer will verify OS support, CPU cores, memory and free disk space using real host probes — never canned results."
        action={
          <Button onClick={() => runCheck.mutate()}>Run requirements check</Button>
        }
      />
    </Panel>
  );
}

const severityBadge = {
  ok: ("success" as const),
  warn: ("warning" as const),
  error: ("danger" as const),
};

function RequirementsReportView({ report }: { report: RequirementsReport }) {
  const blocking = report.checks.some((c) => c.severity === "error");
  const warnings = report.checks.filter((c) => c.severity === "warn").length;

  return (
    <>
      <Panel title="System requirements">
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <Badge tone={blocking ? "danger" : "success"}>
            {blocking ? "Blocking issues found" : "All required checks passed"}
          </Badge>
          {warnings > 0 && <Badge tone="warning">{warnings} warning(s)</Badge>}
          <Badge tone="neutral">
            {report.os}/{report.arch}
          </Badge>
        </div>

        <ul role="list" className="divide-y divide-slate-100 rounded-lg border border-slate-100">
          {report.checks.map((c) => (
            <li key={c.name} className="flex items-center justify-between gap-4 px-4 py-3">
              <div>
                <p className="text-sm font-medium capitalize text-slate-800">{c.name.replace(/_/g, " ")}</p>
                {c.message && <p className="mt-0.5 text-xs text-slate-500">{c.message}</p>}
              </div>
              <div className="flex items-center gap-3">
                {c.value && <span className="font-mono text-xs text-slate-500">{c.value}</span>}
                <Badge tone={severityBadge[c.severity]}>{c.severity}</Badge>
              </div>
            </li>
          ))}
        </ul>
      </Panel>
      {blocking && (
        <Alert tone="danger" title="Installation cannot continue">
          Resolve blocking requirement failures before proceeding.
        </Alert>
      )}
    </>
  );
}

function StepLicense({ ctx }: { ctx: Ctx }) {
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);

  const activate = useMutation({
    mutationFn: () => installerApi.license(key.trim()),
    onSuccess: () => void ctx.goTo("database"),
    onError: (err) =>
      setError(err instanceof Error ? err.message : "Activation failed"),
  });

  return (
    <>
      <Panel title="Activate your license">
        <Alert tone="info">
          EpicPanel is a commercial product. Your license key is validated against the licensing
          server and bound to this installation's unique fingerprint.
        </Alert>
        <form
          className="mt-4 space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            activate.mutate();
          }}
        >
          <Input
            label="License key"
            placeholder="EPIC-XXXX-XXXX-XXXX"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            error={error ?? undefined}
          />
          <Button type="submit" loading={activate.isPending} className="w-full">
            Validate &amp; activate
          </Button>
        </form>
      </Panel>
    </>
  );
}

function StepDatabase(_props: { ctx: Ctx }) {
  const [testResult, setTestResult] = useState<{ ok: boolean; message?: string } | null>(null);
  const [overrideMode, setOverrideMode] = useState(false);
  const [overrideDsn, setOverrideDsn] = useState("");

  const test = useMutation({
    mutationFn: () => installerApi.databaseTest(),
    onSuccess: (res) =>
      setTestResult({ ok: res.reachable, message: res.message }),
    onError: (err) => setTestResult({ ok: false, message: err instanceof Error ? err.message : "Test failed" }),
  });
  const applyOverride = useMutation({
    mutationFn: () => installerApi.databaseConfig(overrideDsn.trim()),
    onSuccess: () => setOverrideMode(false),
    onError: (err) =>
      setTestResult({
        ok: false,
        message: err instanceof Error ? err.message : "Could not save connection",
      }),
  });

  return (
    <Panel title="Database configuration">
      <p className="mb-4 text-sm leading-relaxed text-slate-600">
        This API instance is already connected to its PostgreSQL backend. Verify reachability now;
        credentials are supplied through environment or config files so they never transit the
        browser.
      </p>

      {testResult && (
        <div className="mb-4">
          {testResult.ok ? (
            <Alert tone="success">Connection verified — PostgreSQL is reachable.</Alert>
          ) : (
            <Alert tone="danger" title="Unreachable">{testResult.message}</Alert>
          )}
        </div>
      )}

      <Button
        variant="secondary"
        loading={test.isPending}
        onClick={() => {
          setTestResult(null);
          test.mutate();
        }}
      >
        Test configured connection
      </Button>

      <div className="mt-6 border-t border-dashed border-slate-200 pt-5">
        <button
          onClick={() => setOverrideMode((v) => !v)}
          className="focus-ring text-sm font-medium text-indigo-600 hover:underline"
        >
          {overrideMode ? "Hide" : "Show"} alternative connection (advanced)
        </button>
        {overrideMode && (
          <form
            className="mt-3 space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              applyOverride.mutate();
            }}
          >
            <Input
              label="PostgreSQL connection URL"
              placeholder="postgres://user:password@host:5432/epicpanel"
              value={overrideDsn}
              onChange={(e) => setOverrideDsn(e.target.value)}
              hint="Verified, then written to the server-side config file. Requires a panel restart to adopt."
            />
            <Button type="submit" size="sm" loading={applyOverride.isPending}>
              Save connection
            </Button>
            {applyOverride.isSuccess && (
              <Alert tone="warning" title="Restart required" >
                Connection stored. Restart EpicPanel, then reload this page.
              </Alert>
            )}
          </form>
        )}
      </div>
    </Panel>
  );
}

function StepConfiguration({ ctx }: { ctx: Ctx }) {
  const [siteName, setSiteName] = useState("");
  const [timezone, setTimezone] = useState(Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: () => installerApi.configuration(siteName.trim(), timezone),
    onSuccess: () => void ctx.goTo("administrator"),
    onError: (err) => setError(err instanceof Error ? err.message : "Save failed"),
  });

  return (
    <Panel title="Panel configuration">
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <Input
          label="Panel name"
          placeholder="My hosting panel"
          value={siteName}
          onChange={(e) => setSiteName(e.target.value)}
          hint="Shown in emails, titles and reports."
          autoFocus
        />
        <Input
          label="Timezone"
          value={timezone}
          onChange={(e) => setTimezone(e.target.value)}
          hint="IANA timezone identifier used for scheduling."
        />
        {error && <Alert tone="danger">{error}</Alert>}
        <Button type="submit" loading={save.isPending} className="w-full">
          Save configuration
        </Button>
      </form>
    </Panel>
  );
}

interface AdminForm {
  username: string;
  email: string;
  password: string;
  confirm_password: string;
}

function StepAdministrator({ ctx }: { ctx: Ctx }) {
  const [serverError, setServerError] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<AdminForm>({
    mode: "onTouched",
    defaultValues: { username: "", email: "", password: "", confirm_password: "" },
  });

  const create = useMutation({
    mutationFn: (data: AdminForm) =>
      installerApi.administrator({
        username: data.username,
        email: data.email || undefined,
        display_name: data.username,
        password: data.password,
        confirm_password: data.confirm_password,
      }),
    onSuccess: () => void ctx.goTo("security"),
    onError: (err) => setServerError(err instanceof Error ? err.message : "Creation failed"),
  });

  const password = watch("password");

  return (
    <Panel title="Create the first administrator">
      <Alert tone="info">
        The first account receives the built-in <strong>super_admin</strong> role through the RBAC
        system. Password policy is enforced by the server.
      </Alert>
      <form
        className="mt-4 space-y-4"
        onSubmit={handleSubmit((d) => {
          setServerError(null);
          create.mutate(d);
        })}
        noValidate
      >
        <Input
          label="Username"
          autoComplete="username"
          error={errors.username?.message}
          {...register("username", {
            required: "Username is required",
            minLength: { value: 3, message: "Minimum 3 characters" },
            pattern: { value: /^[a-zA-Z0-9._-]+$/, message: "Letters, digits, dot, dash, underscore only" },
          })}
        />
        <Input
          label="Email (optional)"
          type="email"
          autoComplete="email"
          error={errors.email?.message}
          {...register("email")}
        />
        <PasswordInput
          label="Password"
          autoComplete="new-password"
          showMeter
          error={errors.password?.message}
          {...register("password", { required: "Password is required", minLength: { value: 12, message: "Minimum 12 characters" } })}
        />
        <PasswordInput
          label="Confirm password"
          autoComplete="new-password"
          error={errors.confirm_password?.message}
          {...register("confirm_password", {
            validate: (v) => v === password || "Passwords do not match",
          })}
        />
        {serverError && <Alert tone="danger">{serverError}</Alert>}
        <Button type="submit" loading={create.isPending} className="w-full">
          Create administrator
        </Button>
      </form>
    </Panel>
  );
}

function StepSecurity({ ctx }: { ctx: Ctx }) {
  const defaults = { min: 12, fails: 10, lockout: 15, lifetime: 1440 };
  const [minLen, setMinLen] = useState(defaults.min);
  const [fails, setFails] = useState(defaults.fails);
  const [lockout, setLockout] = useState(defaults.lockout);
  const [lifetime, setLifetime] = useState(defaults.lifetime);
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: () =>
      installerApi.security({
        min_password_length: minLen,
        max_failed_logins: fails,
        lockout_minutes: lockout,
        session_lifetime_minutes: lifetime,
      }),
    onSuccess: () => void ctx.goTo("finish"),
    onError: (err) => setError(err instanceof Error ? err.message : "Save failed"),
  });

  return (
    <Panel title="Security configuration">
      <form
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <NumberField label="Min password length" value={minLen} onChange={setMinLen} min={10} max={128} />
        <NumberField label="Failed logins before lockout" value={fails} onChange={setFails} min={3} max={1000} />
        <NumberField label="Lockout duration (minutes)" value={lockout} onChange={setLockout} min={1} max={14400} />
        <NumberField label="Session lifetime (minutes)" value={lifetime} onChange={setLifetime} min={30} max={43200} />
        {error && <div className="sm:col-span-2"><Alert tone="danger">{error}</Alert></div>}
        <div className="sm:col-span-2">
          <Button type="submit" loading={save.isPending} className="w-full">
            Apply security settings
          </Button>
        </div>
      </form>
    </Panel>
  );
}

function NumberField({
  label,
  value,
  onChange,
  min,
  max,
}: {
  label: string;
  value: number;
  onChange: (n: number) => void;
  min: number;
  max: number;
}) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-sm font-medium text-slate-700">{label}</span>
      <input
        type="number"
        className="block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
        value={value}
        min={min}
        max={max}
        onChange={(e) => onChange(Number(e.target.value))}
      />
    </label>
  );
}

function StepFinish({ ctx }: { ctx: Ctx }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);

  const complete = useMutation({
    mutationFn: () => installerApi.complete(),
    onSuccess: async () => {
      await queryClient.invalidateQueries(); // installed flag flips everywhere
      navigate("/login", { replace: true });
    },
    onError: (err) => setError(err instanceof Error ? err.message : "Completion failed"),
  });

  return (
    <>
      <Panel title="Ready to finish">
        <ul role="list" className="space-y-2 text-sm">
          {STEPS.slice(0, 6).map((s) => (
            <li key={s.id} className="flex items-center gap-2 text-slate-700">
              <CheckCircleSmall done={Boolean(ctx.status.steps[s.id])} /> {s.title}
            </li>
          ))}
        </ul>
        <Alert tone="info" title="This action is irreversible">
          After completion the installer becomes permanently inaccessible on the backend.
        </Alert>
        {error && <Alert tone="danger">{error}</Alert>}
        <Button
          className="mt-4 w-full"
          variant="danger"
          loading={complete.isPending}
          onClick={() => complete.mutate()}
        >
          Initialize EpicPanel
        </Button>
      </Panel>
    </>
  );
}

function CheckCircleSmall({ done }: { done: boolean }) {
  return done ? (
    <span className="grid h-5 w-5 place-items-center rounded-full bg-emerald-100 text-emerald-600">
      <Check className="h-3 w-3" />
    </span>
  ) : (
    <span className="grid h-5 w-5 place-items-center rounded-full bg-slate-100 text-slate-400">
      <ShieldCheck className="h-3 w-3" />
    </span>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardBody>
        <h2 className="mb-4 flex items-center gap-2 text-base font-semibold text-slate-900">
          <TerminalSquare className="h-4 w-4 text-indigo-500" aria-hidden />
          {title}
        </h2>
        {children}
      </CardBody>
    </Card>
  );
}
