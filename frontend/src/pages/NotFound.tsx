import { Link } from "react-router-dom";

export function NotFoundPage() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-slate-100 px-4 text-center">
      <p className="text-5xl font-bold text-slate-300">404</p>
      <h1 className="text-lg font-semibold text-slate-800">Page not found</h1>
      <p className="text-sm text-slate-500">The page you requested does not exist.</p>
      <Link to="/" className="focus-ring rounded-lg font-medium text-indigo-600 hover:underline">
        Return home
      </Link>
    </div>
  );
}
