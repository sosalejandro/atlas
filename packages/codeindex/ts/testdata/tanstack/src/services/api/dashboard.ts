import { apiClient } from '../client';

export const dashboardApi = {
  getSummary() {
    return apiClient.get('/api/v1/dashboard/summary');
  },
};
