import { createBrowserRouter } from 'react-router-dom';
import { lazy } from 'react';

const SubscriptionsPage = lazy(() => import('./pages/SubscriptionsPage'));

export const router = createBrowserRouter([
  {
    path: '/subscriptions',
    element: <SubscriptionsPage />,
  },
]);
