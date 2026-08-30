import { describe, expect, it } from "vitest";
import {
  buildProvisionRequest,
  eligibleAliasDomains,
  eligiblePrimaryDomains,
  isStepComplete,
  siteDocumentRoot,
  sortedPHPVersions,
  usableServers,
  type WizardData,
} from "./wizardLogic";
import type { DomainView, PHPVersionInfo, ServerView } from "../../types/api";

const domain = (over: Partial<DomainView>): DomainView => ({
  id: "d1",
  server_id: "s1",
  domain: "example.com",
  type: "primary",
  status: "active",
  website_id: null,
  created_at: "2026-01-01",
  updated_at: "2026-01-01",
  ...over,
});

const server = (over: Partial<ServerView>): ServerView => ({
  id: "s1",
  label: "web-01",
  hostname: "web01",
  os: "linux",
  os_version: "Ubuntu 24.04",
  arch: "amd64",
  agent_version: "0.2.0",
  status: "online",
  registered_ip: "10.0.0.5",
  registered_at: "2026-01-01",
  last_seen_at: "2026-08-29",
  agent_url: "http://10.0.0.5:9200",
  manageable: true,
  online: true,
  ...over,
});

describe("eligiblePrimaryDomains", () => {
  it("returns unattached non-alias domains for the server", () => {
    const domains = [
      domain({ id: "a", server_id: "s1" }),
      domain({ id: "b", server_id: "s2" }), // other server
      domain({ id: "c", website_id: "w1" }), // attached
      domain({ id: "d", type: "alias" }), // alias type
      domain({ id: "e", domain: "*.example.com" }), // wildcard
    ];
    const got = eligiblePrimaryDomains(domains, "s1");
    expect(got.map((d) => d.id)).toEqual(["a"]);
  });
});

describe("eligibleAliasDomains", () => {
  it("returns unattached alias domains only", () => {
    const domains = [
      domain({ id: "a", type: "alias", domain: "alias.example.com" }),
      domain({ id: "b", type: "alias", website_id: "w9" }),
      domain({ id: "c", type: "primary" }),
    ];
    expect(eligibleAliasDomains(domains, "s1").map((d) => d.id)).toEqual(["a"]);
  });
});

describe("usableServers", () => {
  it("filters out servers without a management channel", () => {
    const servers = [server({ id: "ok" }), server({ id: "legacy", manageable: false })];
    expect(usableServers(servers).map((s) => s.id)).toEqual(["ok"]);
  });
});

describe("sortedPHPVersions", () => {
  it("sorts newest first", () => {
    const versions: PHPVersionInfo[] = [
      { version: "8.2", binary_path: "x", handler_type: "fpm", status: "available" },
      { version: "8.4", binary_path: "x", handler_type: "fpm", status: "available" },
      { version: "8.3", binary_path: "x", handler_type: "fpm", status: "available" },
    ];
    expect(sortedPHPVersions(versions).map((v) => v.version)).toEqual(["8.4", "8.3", "8.2"]);
  });
});

describe("siteDocumentRoot", () => {
  it("uses the fixed hosting convention", () => {
    expect(siteDocumentRoot("linux", "example.com")).toBe("/www/wwwroot/example.com/public");
    expect(siteDocumentRoot("windows", "example.com")).toBe("C:\\www\\wwwroot\\example.com\\public");
  });
});

describe("isStepComplete", () => {
  const base: WizardData = {
    serverId: "s1",
    domainId: "d1",
    phpVersion: "8.4",
    aliasIds: [],
  };
  it("requires server + domain selection on step 0", () => {
    expect(isStepComplete(0, base)).toBe(true);
    expect(isStepComplete(0, { ...base, domainId: "" })).toBe(false);
    expect(isStepComplete(0, { ...base, serverId: "" })).toBe(false);
  });
  it("runtime step passes without PHP (static allowed)", () => {
    expect(isStepComplete(1, { ...base, phpVersion: "" })).toBe(true);
  });
});

describe("buildProvisionRequest", () => {
  it("never sends a document root — the backend derives the fixed layout", () => {
    const req = buildProvisionRequest({
      serverId: "s1",
      domainId: "d1",
      phpVersion: "8.4",
      aliasIds: ["a1", "a2"],
    });
    expect(req).toEqual({
      server_id: "s1",
      domain_id: "d1",
      alias_domain_ids: ["a1", "a2"],
      php_version: "8.4",
    });
    expect("document_root" in req).toBe(false);
  });
});
