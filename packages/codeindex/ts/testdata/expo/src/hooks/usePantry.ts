import { useQuery } from '@tanstack/react-query';
import { pantryApi } from '../services/api/pantry';

export function usePantry() {
  return useQuery({
    queryKey: ['pantry'],
    queryFn: () => pantryApi.list(),
  });
}
