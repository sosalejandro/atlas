import { useMutation } from '@tanstack/react-query';
import { authApi } from '../services/api/auth';

export function useLogin() {
  return useMutation({
    mutationFn: (input: { email: string; password: string }) =>
      authApi.login(input.email, input.password),
  });
}
