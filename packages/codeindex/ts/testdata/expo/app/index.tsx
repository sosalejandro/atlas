// @atlas:feature mobile.home
import { usePantry } from '../src/hooks/usePantry';

export default function HomeScreen() {
  const { data } = usePantry();
  return <div>{data ? 'loaded' : 'loading'}</div>;
}
