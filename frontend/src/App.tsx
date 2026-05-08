import Hello from './pages/Hello';
import { ENV } from './api';

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
        style={{ borderBottom: '1px solid #ddd', marginBottom: '1.5rem', paddingBottom: '0.5rem' }}
      >
        <h1 style={{ margin: 0 }}>website-agency</h1>
        <p style={{ margin: '0.25rem 0 0', color: '#666' }}>
          Skeleton — environment{' '}
          <code style={{ background: '#f3f3f3', padding: '0.1rem 0.4rem', borderRadius: 3 }}>
            {ENV}
          </code>
        </p>
      </header>
      <Hello />
    </div>
  );
}
