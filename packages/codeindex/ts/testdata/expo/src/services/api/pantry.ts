import { apiClient } from '../client';

export const pantryApi = {
  list() {
    return apiClient.get('/api/v1/pantry');
  },
};
