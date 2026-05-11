import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Route, Routes } from 'react-router-dom';
import App from './App';
import AuthGuard from './AuthGuard';
import { BASENAME } from './api';
import Dashboard from './pages/Dashboard';
import Login from './pages/Login';
import Queue from './pages/Queue';
import Settings from './pages/Settings';

const root = document.getElementById('root');
if (!root) throw new Error('Missing #root element in index.html');

createRoot(root).render(
  <StrictMode>
    <BrowserRouter basename={BASENAME}>
      <Routes>
        <Route path="/" element={<App />}>
          {/* /login is public — needs to be reachable when unauthenticated. */}
          <Route path="login" element={<Login />} />
          {/* All other routes pass through AuthGuard, which bounces
              unauthenticated callers to the Cognito Hosted UI. */}
          <Route element={<AuthGuard />}>
            <Route index element={<Dashboard />} />
            <Route path="queue" element={<Queue />} />
            <Route path="settings" element={<Settings />} />
          </Route>
        </Route>
      </Routes>
    </BrowserRouter>
  </StrictMode>
);
