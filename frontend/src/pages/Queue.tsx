import AccessStrip from '../components/AccessStrip';

// Queue is the operator's review surface for generated previews.
// Iter 6.1+ wires it to GET /queue (paginated by priority); for now
// it's an empty list placeholder so the route exists in 0.G.1. The
// access-strip skeleton lands here in empty-state in iter 0.G.4 so
// the layout is visible end-to-end before the queue-item detail
// page in iter 6 wires real props in.
export default function Queue() {
  return (
    <div>
      <h2>Review queue</h2>
      <p style={{ color: '#666' }}>
        No items yet. Once the discover → audit → spec → generator → publisher chain produces
        previews, they'll surface here for operator review.
      </p>
      <h3 style={{ fontSize: '1rem', marginTop: '1.5rem' }}>Access strip preview (empty state)</h3>
      <AccessStrip />
    </div>
  );
}
