import { useQuery } from '@tanstack/react-query';
import { subscriptionApi } from '../features/subscriptions/api';

export function useSubscriptions() {
  return useQuery({
    queryKey: ['subscriptions'],
    queryFn: () => subscriptionApi.list(),
  });
}
