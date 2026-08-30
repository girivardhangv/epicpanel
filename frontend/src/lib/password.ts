/** Heuristic strength score 0–4 (-1 == empty) used by the password meter. */
export function passwordStrength(pw: string): number {
  if (!pw) return -1;
  let score = 0;
  if (pw.length >= 12) score++;
  else if (pw.length >= 8) score += 0.5;
  const classes =
    [/[a-z]/, /[A-Z]/, /[0-9]/, /[^a-zA-Z0-9]/].filter((re) => re.test(pw)).length;
  score += classes * 0.75;
  if (pw.length >= 16) score += 0.5;
  return Math.max(0, Math.min(4, Math.round(score)));
}

export const strengthLabels = ["Too weak", "Weak", "Fair", "Strong", "Excellent"];

export const strengthColorClasses = [
  "bg-red-500",
  "bg-orange-500",
  "bg-yellow-500",
  "bg-emerald-500",
  "bg-emerald-600",
];
