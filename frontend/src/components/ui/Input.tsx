import { forwardRef, useId, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { cn } from "../../lib/utils";
import {
  passwordStrength,
  strengthLabels,
  strengthColorClasses,
} from "../../lib/password";

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label: string;
  error?: string;
  hint?: string;
}

const baseField =
  "block w-full rounded-lg border bg-white px-3 py-2 text-sm text-slate-900 placeholder:text-slate-400 shadow-sm transition-colors focus-ring";

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, error, hint, className, id, ...props }, ref) => {
    const autoId = useId();
    const inputId = id ?? `input-${autoId}`;
    return (
      <div className="w-full">
        <label htmlFor={inputId} className="mb-1.5 block text-sm font-medium text-slate-700">
          {label}
        </label>
        <input
          ref={ref}
          id={inputId}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${inputId}-error` : hint ? `${inputId}-hint` : undefined}
          className={cn(
            baseField,
            error ? "border-red-400 focus-visible:ring-red-500" : "border-slate-300",
            className,
          )}
          {...props}
        />
        {hint && !error && (
          <p id={`${inputId}-hint`} className="mt-1.5 text-xs text-slate-500">
            {hint}
          </p>
        )}
        {error && (
          <p id={`${inputId}-error`} role="alert" className="mt-1.5 text-xs font-medium text-red-600">
            {error}
          </p>
        )}
      </div>
    );
  },
);
Input.displayName = "Input";

const meterPct = (score: number) => ((score + 1) / 5) * 100;

export interface PasswordInputProps extends Omit<InputProps, "type"> {
  showMeter?: boolean;
}

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  ({ label, error, hint, showMeter = false, value, className, ...props }, ref) => {
    const [visible, setVisible] = useState(false);
    const pw = typeof value === "string" ? value : "";
    const score = passwordStrength(pw);

    return (
      <div>
        <label
          htmlFor={props.id ?? "password"}
          className="mb-1.5 block text-sm font-medium text-slate-700"
        >
          {label}
        </label>
        <div className="relative">
          <input
            type={visible ? "text" : "password"}
            ref={ref}
            aria-invalid={error ? true : undefined}
            className={cn(
              baseField,
              "pr-10",
              error ? "border-red-400 focus-visible:ring-red-500" : "border-slate-300",
              className,
            )}
            value={value}
            {...props}
          />
          <button
            type="button"
            onClick={() => setVisible((v) => !v)}
            tabIndex={-1}
            aria-label={visible ? "Hide password" : "Show password"}
            className="absolute inset-y-0 right-0 flex items-center pr-3 text-slate-400 hover:text-slate-600"
          >
            {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>

        {showMeter && pw.length > 0 && score >= 0 && (
          <div className="mt-2">
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-slate-200">
              <div
                className={cn("h-full rounded-full transition-all", strengthColorClasses[score])}
                style={{ width: `${meterPct(score)}%` }}
              />
            </div>
            <p
              className={cn(
                "mt-1 text-xs font-medium",
                strengthColorClasses[score].replace("bg-", "text-"),
              )}
            >
              {strengthLabels[score]}
            </p>
          </div>
        )}

        {hint && !error && <p className="mt-1.5 text-xs text-slate-500">{hint}</p>}
        {error && (
          <p role="alert" className="mt-1.5 text-xs font-medium text-red-600">
            {error}
          </p>
        )}
      </div>
    );
  },
);
PasswordInput.displayName = "PasswordInput";
