import { createFileRoute } from '@tanstack/react-router';

// @atlas:feature home.landing
function HomePage() {
  return <div>Home</div>;
}

export const Route = createFileRoute('/')({
  component: HomePage,
});
