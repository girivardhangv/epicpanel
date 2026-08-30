import { NavLink } from "react-router-dom";
import {
  LayoutDashboard,
  Server,
  Globe,
  AtSign,
  Database,
  KeyRound,
  LogOut,
  UserCog,
  Users,
  ShieldCheck,
  ServerCog,
} from "lucide-react";
import type { Permission } from "../types/api";
import { useAuth } from "../features/auth/AuthContext";

interface NavItem {
  to: string;
  label: string;
  permission?: Permission;
  icon: React.ReactNode;
  disabled?: boolean; // modules that ship in a later phase; shown honestly
}

const NAV_SECTIONS: Array<{ title: string; items: NavItem[] }> = [
  {
    title: "Operations",
    items: [
      { to: "/app", label: "Dashboard", permission: "dashboard.view", icon: <LayoutDashboard className="h-4 w-4" /> },
      { to: "/app/servers", label: "Servers", permission: "server.view", icon: <Server className="h-4 w-4" /> },
    ],
  },
  {
    title: "Hosting",
    items: [
      {
        to: "/app/websites",
        label: "Websites",
        permission: "websites.view",
        icon: <Globe className="h-4 w-4" />,
      },
      {
        to: "/app/domains",
        label: "Domains",
        permission: "domains.view",
        icon: <AtSign className="h-4 w-4" />,
      },
      {
        to: "/app/databases",
        label: "Databases",
        permission: "databases.view",
        icon: <Database className="h-4 w-4" />,
      },
    ],
  },
  {
    title: "Product",
    items: [
      { to: "/app/license", label: "License", permission: "license.view", icon: <KeyRound className="h-4 w-4" /> },
    ],
  },
  {
    title: "Administration",
    items: [
      { to: "/app/users", label: "Users", permission: "users.view", icon: <Users className="h-4 w-4" /> },
      { to: "/app/roles", label: "Roles", permission: "roles.view", icon: <ShieldCheck className="h-4 w-4" /> },
      { to: "/app/settings", label: "Settings", permission: "settings.view", icon: <ServerCog className="h-4 w-4" /> },
    ],
  },
];

export function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full min-h-screen">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className="min-h-0 flex-1 overflow-y-auto bg-slate-100 p-6 lg:p-8">{children}</main>
      </div>
    </div>
  );
}

function Sidebar() {
  const { user } = useAuth();
  const perms = user?.permissions ?? [];

  return (
    <aside className="hidden w-60 shrink-0 flex-col bg-slate-900 md:flex">
      <div className="flex h-16 items-center gap-2.5 border-b border-slate-800 px-5">
        <Logo />
        <span className="text-sm font-semibold tracking-wide text-white">EpicPanel</span>
      </div>

      <nav aria-label="Primary" className="flex-1 space-y-6 overflow-y-auto px-3 py-5">
        {NAV_SECTIONS.map((section) => {
          const visible = section.items.filter(
            (i) => !i.permission || perms.includes(i.permission),
          );
          if (visible.length === 0) return null;
          return (
            <div key={section.title}>
              <p className="px-2 pb-2 text-[11px] font-semibold uppercase tracking-widest text-slate-500">
                {section.title}
              </p>
              <ul className="space-y-0.5">
                {visible.map((item) => (
                  <li key={item.to}>
                    {item.disabled ? (
                      <span
                        title="This module ships in a later phase"
                        aria-disabled
                        className="flex cursor-not-allowed items-center gap-3 rounded-lg px-2 py-2 text-sm text-slate-600"
                      >
                        {item.icon}
                        {item.label}
                      </span>
                    ) : (
                      <NavLink
                        to={item.to}
                        end={item.to === "/app"}
                        className={({ isActive }) =>
                          [
                            "flex items-center gap-3 rounded-lg px-2 py-2 text-sm transition-colors focus-ring",
                            isActive
                              ? "bg-slate-800 font-medium text-white"
                              : "text-slate-400 hover:bg-slate-800/60 hover:text-slate-200",
                          ].join(" ")
                        }
                      >
                        {item.icon}
                        {item.label}
                      </NavLink>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </nav>

      <div className="border-t border-slate-800 p-4">
        <a
          href="/healthz"
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-2 text-xs text-slate-600 hover:text-slate-400"
        >
          <ServerCog className="h-3.5 w-3.5" /> API status
        </a>
      </div>
    </aside>
  );
}

function Topbar() {
  return (
    <header className="flex h-16 items-center justify-end gap-4 border-b border-slate-200 bg-white px-6">
      <UserMenu />
    </header>
  );
}

function UserMenu() {
  const { user, logout } = useAuth();
  if (!user) return null;

  return (
    <div className="flex items-center gap-2">
      <div className="mr-2 text-right">
        <p className="text-sm font-medium text-slate-800">{user.display_name || user.username}</p>
        <p className="text-xs text-slate-500">{user.email ?? user.username}</p>
      </div>
      <NavLink
        to="/profile"
        aria-label="Your profile"
        title="Profile & password"
        className={({ isActive }) =>
          [
            "focus-ring inline-flex h-9 w-9 items-center justify-center rounded-lg transition-colors",
            isActive
              ? "bg-indigo-50 text-indigo-600"
              : "text-slate-500 hover:bg-slate-100 hover:text-slate-700",
          ].join(" ")
        }
      >
        <UserCog className="h-4 w-4" />
      </NavLink>
      <button
        onClick={() => void logout()}
        aria-label={`Sign out ${user.username}`}
        title="Sign out"
        className="focus-ring inline-flex h-9 w-9 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-100 hover:text-slate-700"
      >
        <LogOut className="h-4 w-4" />
      </button>
    </div>
  );
}

export function Logo() {
  return (
    <div className="grid h-8 w-8 place-items-center rounded-lg bg-gradient-to-br from-indigo-500 to-violet-600 shadow-inner">
      <svg viewBox="0 0 24 24" fill="none" className="h-4 w-4">
        <path d="M4 17V7l8-3 8 3v10l-8 3-8-3z" stroke="white" strokeWidth="1.8" strokeLinejoin="round" />
        <path d="M12 10v8m0-8L4 7m8 3 8-3" stroke="rgba(255,255,255,.55)" strokeWidth="1.4" strokeLinejoin="round" />
      </svg>
    </div>
  );
}
