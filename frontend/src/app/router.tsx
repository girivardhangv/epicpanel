import { createBrowserRouter, Navigate } from "react-router-dom";
import { AuthProvider } from "../features/auth/AuthContext";
import { BootstrapGate, ProtectedArea } from "../components/Guards";
import { LoginPage } from "../pages/LoginPage";
import { InstallerWizardPage } from "../pages/installer/InstallerWizard";
import { DashboardPage } from "../pages/DashboardPage";
import { ServersPage } from "../pages/ServersPage";
import { ServerDetailPage } from "../pages/servers/ServerDetailPage";
import { LicensePage } from "../pages/LicensePage";
import { UsersPage } from "../pages/UsersPage";
import { RolesPage } from "../pages/RolesPage";
import { ProfilePage } from "../pages/ProfilePage";
import { SettingsPage } from "../pages/SettingsPage";
import { DomainsPage } from "../pages/DomainsPage";
import { DatabasesPage } from "../pages/DatabasesPage";
import { DatabaseDetailPage } from "../pages/databases/DatabaseDetailPage";
import { WebsitesPage } from "../pages/WebsitesPage";
import { CreateWebsiteWizardPage } from "../pages/websites/CreateWizardPage";
import { WebsiteDetailPage } from "../pages/websites/WebsiteDetailPage";
import { NotFoundPage } from "../pages/NotFound";

export const router = createBrowserRouter([
  { path: "/", element: <BootstrapGate /> },
  {
    path: "/login",
    element: (
      <AuthProvider>
        <LoginPage />
      </AuthProvider>
    ),
  },
  { path: "/install", element: <InstallerWizardPage /> },
  {
    path: "/app",
    element: (
      <AuthProvider>
        <ProtectedArea />
      </AuthProvider>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "servers", element: <ServersPage /> },
      { path: "servers/:id", element: <ServerDetailPage /> },
      { path: "websites", element: <WebsitesPage /> },
      { path: "websites/new", element: <CreateWebsiteWizardPage /> },
      { path: "websites/:id", element: <WebsiteDetailPage /> },
      { path: "domains", element: <DomainsPage /> },
      { path: "databases", element: <DatabasesPage /> },
      { path: "databases/:id", element: <DatabaseDetailPage /> },
      { path: "license", element: <LicensePage /> },
      { path: "users", element: <UsersPage /> },
      { path: "roles", element: <RolesPage /> },
      { path: "settings", element: <SettingsPage /> },
      { path: "profile", element: <ProfilePage /> },
    ],
  },
  // Legacy aliases keep short paths ("/servers", "/users", …) working.
  { path: "/servers", element: <Navigate to="/app/servers" replace /> },
  { path: "/license", element: <Navigate to="/app/license" replace /> },
  { path: "/users", element: <Navigate to="/app/users" replace /> },
  { path: "/roles", element: <Navigate to="/app/roles" replace /> },
  { path: "/profile", element: <Navigate to="/app/profile" replace /> },
  { path: "/websites", element: <Navigate to="/app/websites" replace /> },
  { path: "/domains", element: <Navigate to="/app/domains" replace /> },
  { path: "/databases", element: <Navigate to="/app/databases" replace /> },
  { path: "/dashboard", element: <Navigate to="/app" replace /> },
  { path: "*", element: <NotFoundPage /> },
]);
