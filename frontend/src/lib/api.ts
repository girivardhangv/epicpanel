/**
 * Typed HTTP client for the EpicPanel API.
 *
 * - Session cookie is httpOnly, managed entirely by the server.
 * - CSRF token lives in a readable cookie and is echoed via X-CSRF-Token.
 * - Errors arrive as { error: { code, message } } and are normalized into ApiError.
 */

export class ApiError extends Error {
  code: string;
  status: number;
  requestId?: string;

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }
}

function readCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
  return match ? decodeURIComponent(match[1]) : null;
}

const BASE = "/api/v1";

export async function request<T>(
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  options: { body?: unknown; signal?: AbortSignal } = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (options.body !== undefined) headers["Content-Type"] = "application/json";

  if (method !== "GET") {
    const csrf = readCookie("epicpanel_csrf");
    if (csrf) headers["X-CSRF-Token"] = csrf;
  }

  let res: Response;
  try {
    res = await fetch(BASE + path, {
      method,
      credentials: "same-origin",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
    });
  } catch (err) {
    throw new ApiError(0, "NETWORK_ERROR", err instanceof Error ? err.message : "Network error");
  }

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let payload: unknown = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      /* non-JSON error body */
    }
  }

  if (!res.ok) {
    type ErrorShape = { error?: { code?: string; message?: string } };
    const shaped = (payload ?? {}) as ErrorShape;
    throw new ApiError(
      res.status,
      shaped.error?.code ?? `HTTP_${res.status}`,
      shaped.error?.message ?? (res.statusText || "Request failed"),
    );
  }

  return payload as T;
}

export const get = <T>(path: string, opts?: { signal?: AbortSignal }) =>
  request<T>("GET", path, opts);
export const post = <T>(path: string, body?: unknown) => request<T>("POST", path, { body });
export const del = <T>(path: string) => request<T>("DELETE", path);
