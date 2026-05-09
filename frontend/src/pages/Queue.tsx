// Queue is the operator's review surface for generated previews.
// Iter 6.1+ wires it to GET /queue (paginated by priority); for now
// it's an empty list placeholder so the route exists in 0.G.1.
export default function Queue() {
  return (
    <div>
      <h2>Review queue</h2>
      <p style={{ color: '#666' }}>
        No items yet. Once the discover → audit → spec → generator → publisher
        chain produces previews, they'll surface here for operator review.
      </p>
    </div>
  );
}
