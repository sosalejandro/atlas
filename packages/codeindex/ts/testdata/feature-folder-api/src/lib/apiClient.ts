// Stub HTTP client used by feature api.ts files
export const apiClient = {
  get: (url: string) => fetch(url),
  post: (url: string, body?: unknown) => fetch(url, { method: 'POST', body: JSON.stringify(body) }),
};
