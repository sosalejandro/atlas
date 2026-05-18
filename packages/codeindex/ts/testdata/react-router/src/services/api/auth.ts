import { apiClient } from '../client';

const BASE = '/api/v1/auth';

export const authApi = {
  login(email: string, password: string) {
    return apiClient.post(`${BASE}/login`, { email, password });
  },
  logout() {
    return apiClient.post(`${BASE}/logout`);
  },
};
