import { AlertTriangle, CheckCircle2, Info, ShieldAlert } from "lucide-react";
import { cn } from "../../lib/utils";

type Tone = "info" | "success" | "warning" | "danger";

const toneMap: Record<Tone, { box: string; icon: React.ReactNode }> = {
  info: {
    box: "border-indigo-200 bg-indigo-50 text-indigo-800",
    icon: <Info className="h-5 w-5 text-indigo-500" aria-hidden />,
  },
  success: {
    box: "border-emerald-200 bg-emerald-50 text-emerald-800",
    icon: <CheckCircle2 className="h-5 w-5 text-emerald-500" aria-hidden />,
  },
  warning: {
    box: "border-amber-200 bg-amber-50 text-amber-800",
    icon: <AlertTriangle className="h-5 w-5 text-amber-500" aria-hidden />,
  },
  danger: {
    box: "border-red-200 bg-red-50 text-red-800",
    icon: <ShieldAlert className="h-5 w-5 text-red-500" aria-hidden />,
  },
};

export function Alert({
  tone = "info",
  title,
  children,
}: {
  tone?: Tone;
  title?: string;
  children?: React.ReactNode;
}) {
  const t = toneMap[tone];
  return (
    <div role={tone === "danger" ? "alert" : "status"} className={cn("flex gap-3 rounded-lg border p-4", t.box)}>
      <div className="shrink-0">{t.icon}</div>
      <div className="text-sm">
        {title && <p className="font-semibold">{title}</p>}
        {children && <div className={cn(title && "mt-1")}>{children}</div>}
      </div>
    </div>
  );
}
