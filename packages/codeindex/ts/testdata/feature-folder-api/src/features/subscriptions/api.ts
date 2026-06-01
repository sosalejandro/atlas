import { apiClient } from '../../lib/apiClient';

const BASE = '/api/v1/subscriptions';

export const subscriptionApi = {
  list() {
    return apiClient.get(BASE);
  },
  get(id: string) {
    return apiClient.get(`${BASE}/${id}`);
  },
};
