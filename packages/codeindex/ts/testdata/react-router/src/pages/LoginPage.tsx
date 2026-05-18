import { useLogin } from '../hooks/useLogin';

export default function LoginPage() {
  const login = useLogin();
  return <div onClick={() => login.mutate({ email: 'a', password: 'b' })}>Login</div>;
}
