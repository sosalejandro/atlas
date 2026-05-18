import { createBrowserRouter } from 'react-router-dom';
import { lazy } from 'react';

// @atlas:feature foo.bar
const LoginPage = lazy(() => import('./pages/LoginPage'));
const DashboardPage = lazy(() => import('./pages/DashboardPage'));

export const router = createBrowserRouter([
  {
    path: '/login',
    element: <LoginPage />,
  },
  {
    path: '/dashboard',
    element: <DashboardPage />,
  },
]);
