import { createFileRoute } from '@tanstack/react-router';
import { useDashboard } from '../hooks/useDashboard';

function DashboardPage() {
  const { data } = useDashboard();
  return <div>{data ? 'loaded' : 'loading'}</div>;
}

export const Route = createFileRoute('/dashboard')({
  component: DashboardPage,
});
