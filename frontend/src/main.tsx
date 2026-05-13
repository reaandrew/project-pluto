import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Route, Routes } from 'react-router-dom';
import App from './App';
import AuthGuard from './AuthGuard';
import { BASENAME } from './api';
import Callback from './pages/Callback';
import Dashboard from './pages/Dashboard';
import Login from './pages/Login';
import Metrics from './pages/Metrics';
import Queue from './pages/Queue';
import Settings from './pages/Settings';
import Targeting from './pages/Targeting';

const root = document.getElementById('root');
if (!root) throw new Error('Missing #root element in index.html');

createRoot(root).render(
  <StrictMode>
    <BrowserRouter basename={BASENAME}>
      <Routes>
        <Route path="/" element={<App />}>
          {/* Public routes — reachable when unauthenticated. */}
          <Route path="login" element={<Login />} />
          <Route path="oauth/callback" element={<Callback />} />
          {/* All other routes pass through AuthGuard, which sends
              unauthenticated callers to /login (which then bounces
              to the Cognito Hosted UI after PKCE prep). */}
          <Route element={<AuthGuard />}>
            <Route index element={<Dashboard />} />
            <Route path="queue" element={<Queue />} />
            <Route path="settings" element={<Settings />} />
            <Route path="settings/targeting" element={<Targeting />} />
            <Route path="metrics" element={<Metrics />} />
          </Route>
        </Route>
      </Routes>
    </BrowserRouter>
  </StrictMode>
);
