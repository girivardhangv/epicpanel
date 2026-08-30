import { forwardRef } from "react";
import { cn } from "../../lib/utils";
import { Loader2 } from "lucide-react";

type Variant = "primary" | "secondary" | "danger" | "ghost" | "outline";
type Size = "sm" | "md" | "lg";

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: React.ReactNode;
}

const variantClasses: Record<Variant, string> = {
  primary:
    "bg-indigo-600 text-white hover:bg-indigo-700 active:bg-indigo-800 disabled:bg-indigo-300 shadow-sm",
  secondary:
    "bg-slate-900 text-white hover:bg-slate-700 active:bg-slate-800 disabled:bg-slate-400 shadow-sm",
  danger: "bg-red-600 text-white hover:bg-red-700 disabled:bg-red-300 shadow-sm",
  ghost: "text-slate-700 hover:bg-slate-200/70 disabled:text-slate-400",
  outline:
    "border border-slate-300 bg-white text-slate-700 hover:bg-slate-50 disabled:text-slate-400 shadow-sm",
};

const sizeClasses: Record<Size, string> = {
  sm: "h-8 px-3 text-xs gap-1.5 rounded-md",
  md: "h-10 px-4 text-sm gap-2 rounded-lg",
  lg: "h-11 px-6 text-sm gap-2 rounded-lg",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = "primary", size = "md", loading = false, icon, children, disabled, type, ...props }, ref) => (
    <button
      ref={ref}
      type={type ?? "button"}
      disabled={disabled || loading}
      className={cn(
        "inline-flex items-center justify-center font-medium transition-colors select-none",
        "focus-ring disabled:cursor-not-allowed",
        variantClasses[variant],
        sizeClasses[size],
        className,
      )}
      {...props}
    >
      {loading ? <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" /> : icon}
      {children}
    </button>
  ),
);
Button.displayName = "Button";
