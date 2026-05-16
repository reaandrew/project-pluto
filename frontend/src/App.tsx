import { NavLink, Outlet } from 'react-router-dom';
import { ENV } from './api';

// App is the routed shell — header + nav strip + <Outlet/> for the active
// route. Routing is wired in main.tsx so this component stays presentation-
// only. The four nav links match the four iter-0.G.1 routes; adding a new
// route means one entry here plus one <Route/> in main.tsx.
export default function App() {
  return (
    <div
      style={{
        fontFamily: 'system-ui, sans-serif',
        padding: '2rem',
        maxWidth: 720,
        margin: '0 auto',
      }}
    >
      <header
        style={{ borderBottom: '1px solid #ddd', marginBottom: '1rem', paddingBottom: '0.5rem' }}
      >
        <h1 style={{ margin: 0 }}>ai-website-agency</h1>
        <p style={{ margin: '0.25rem 0 0', color: '#666' }}>
          Admin shell — environment{' '}
          <code style={{ background: '#f3f3f3', padding: '0.1rem 0.4rem', borderRadius: 3 }}>
            {ENV}
          </code>
        </p>
      </header>
      <nav
        style={{
          display: 'flex',
          gap: '1rem',
          marginBottom: '1.5rem',
          paddingBottom: '0.5rem',
          borderBottom: '1px solid #eee',
        }}
      >
        <NavLink to="/" end style={navStyle}>
          Dashboard
        </NavLink>
        <NavLink to="/queue" style={navStyle}>
          Queue
        </NavLink>
        <NavLink to="/replies" style={navStyle}>
          Replies
        </NavLink>
        <NavLink to="/feedback" style={navStyle}>
          Feedback
        </NavLink>
        <NavLink to="/metrics" style={navStyle}>
          Metrics
        </NavLink>
        <NavLink to="/settings" style={navStyle}>
          Settings
        </NavLink>
        <NavLink to="/login" style={navStyle}>
          Sign in
        </NavLink>
      </nav>
      <main>
        <Outlet />
      </main>
    </div>
  );
}

function navStyle({ isActive }: { isActive: boolean }) {
  return {
    textDecoration: 'none',
    color: isActive ? '#0a3' : '#444',
    fontWeight: isActive ? 600 : 400,
  };
}
