// Pure helpers for the website creation wizard — unit-tested, no React.
import type { DomainView, PHPVersionInfo, ServerView } from "../../types/api";

export interface WizardData {
  serverId: string;
  domainId: string;
  phpVersion: string;
  aliasIds: string[];
}

export const WIZARD_STEPS = ["Domain", "Runtime", "Storage", "Review", "Provision", "Complete"] as const;

/** Domains eligible to become a website primary: unattached, not wildcards. */
export function eligiblePrimaryDomains(domains: DomainView[], serverId: string): DomainView[] {
  return domains.filter(
    (d) => d.server_id === serverId && !d.website_id && d.type !== "alias" && !d.domain.includes("*"),
  );
}

/** Alias candidates for the wizard: unattached alias-type domains. */
export function eligibleAliasDomains(domains: DomainView[], serverId: string): DomainView[] {
  return domains.filter((d) => d.server_id === serverId && !d.website_id && d.type === "alias");
}

export function usableServers(servers: ServerView[]): ServerView[] {
  return servers.filter((s) => s.manageable);
}

export function sortedPHPVersions(versions: PHPVersionInfo[]): PHPVersionInfo[] {
  return [...versions].sort((a, b) => b.version.localeCompare(a.version));
}

/**
 * The fixed document root convention (not user-editable): the hosting prefix
 * plus the validated domain slug. Must mirror the panel defaults
 * (websites.DefaultSitesRoot*).
 */
export function siteDocumentRoot(os: string, domain: string): string {
  if (os === "windows") {
    return `C:\\www\\wwwroot\\${domain}\\public`;
  }
  return `/www/wwwroot/${domain}/public`;
}

export function isStepComplete(step: number, data: WizardData): boolean {
  switch (step) {
    case 0: // Domain
      return !!data.serverId && !!data.domainId;
    case 1: // Runtime — PHP optional (static sites allowed)
      return true;
    case 2: // Storage — document root optional (server default)
      return true;
    case 3: // Review
      return !!data.serverId && !!data.domainId;
    default:
      return false;
  }
}

export interface ProvisionRequest {
  server_id: string;
  domain_id: string;
  alias_domain_ids: string[];
  php_version: string;
}

/**
 * Builds the POST /websites payload. The document root is never sent: the
 * backend derives the fixed layout from the validated domain.
 */
export function buildProvisionRequest(data: WizardData): ProvisionRequest {
  return {
    server_id: data.serverId,
    domain_id: data.domainId,
    alias_domain_ids: data.aliasIds,
    php_version: data.phpVersion,
  };
}
