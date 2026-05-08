// Tiny smoke load test invoked from .github/workflows/deploy.yml load-test-k6 job.
// 10 VUs × 30s — enough to exercise warm-path latency without DDoSing our own
// preview env. Keep it small.

import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  vus: 10,
  duration: '30s',
  thresholds: {
    http_req_failed: ['rate<0.05'],
    http_req_duration: ['p(95)<500'],
  },
};

const API_URL = __ENV.API_URL;

export default function () {
  const res = http.get(`${API_URL}/health`);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has env field': (r) => r.json('env') !== undefined,
  });
  sleep(1);
}
