import { useSubscriptions } from '../hooks/useSubscriptions';

export default function SubscriptionsPage() {
  const { data } = useSubscriptions();
  return <div>{JSON.stringify(data)}</div>;
}
