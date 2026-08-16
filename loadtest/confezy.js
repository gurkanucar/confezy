// k6 load test for confezy.
//
// One workload per run, selected with SCENARIO, so the numbers for each are
// unambiguous rather than averaged together:
//
//   poll      GET /v1/snapshot with If-None-Match  → 304, the hot path
//   snapshot  GET /v1/snapshot                     → 200, full payload
//   flags     GET /v1/flags                        → 200
//   write     POST /v1/manage/configs              → 201, exercises the single writer
//   mixed     90% poll, 10% write — writes invalidate the ETag the pollers hold
//
// Usage:
//   k6 run -e READ_KEY=... -e WRITE_KEY=... -e SCENARIO=poll loadtest/confezy.js
//
// run.sh builds a server, seeds it, mints keys and runs every workload.

import http from 'k6/http';
import { check, fail } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const READ_KEY = __ENV.READ_KEY;
const WRITE_KEY = __ENV.WRITE_KEY || READ_KEY;
const SCENARIO = __ENV.SCENARIO || 'poll';
const VUS = Number(__ENV.VUS || 50);
const DURATION = __ENV.DURATION || '30s';

const readHeaders = { 'X-App-Key': READ_KEY };
const writeHeaders = { 'X-App-Key': WRITE_KEY, 'Content-Type': 'application/json' };

export const options = {
  scenarios: {
    [SCENARIO]: {
      executor: 'constant-vus',
      vus: VUS,
      duration: DURATION,
      exec: SCENARIO,
      gracefulStop: '5s',
    },
  },
  // Reported for information; the run is not treated as failed on a miss,
  // because the point here is to measure rather than to gate.
  thresholds: {
    http_req_failed: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  if (!READ_KEY) fail('READ_KEY is required');

  const res = http.get(`${BASE}/v1/snapshot`, { headers: readHeaders });
  if (res.status !== 200) {
    fail(`setup: snapshot returned ${res.status}: ${res.body}`);
  }

  const etag = res.headers['Etag'];
  console.log(`setup: snapshot is ${res.body.length} bytes, ETag ${etag}`);
  return { etag };
}

// poll is what a deployed client actually does: ask with the ETag it holds and
// get a bodiless 304 while nothing has changed.
export function poll(data) {
  const res = http.get(`${BASE}/v1/snapshot`, {
    headers: { ...readHeaders, 'If-None-Match': data.etag },
  });
  check(res, { 'is 304': (r) => r.status === 304 });
}

// snapshot is the cold path: first fetch, or the one after a change.
export function snapshot() {
  const res = http.get(`${BASE}/v1/snapshot`, { headers: readHeaders });
  check(res, { 'is 200': (r) => r.status === 200 });
}

export function flags() {
  const res = http.get(`${BASE}/v1/flags`, { headers: readHeaders });
  check(res, { 'is 200': (r) => r.status === 200 });
}

// write inserts a new config each iteration. Keys are unique per VU and
// iteration so nothing collides, and every insert bumps the environment stamp.
export function write() {
  const key = `k6_${__VU}_${__ITER}`;
  const body = JSON.stringify({
    key,
    value: { vu: __VU, iter: __ITER, note: 'k6 load test' },
    description: 'created by k6',
  });
  const res = http.post(`${BASE}/v1/manage/configs`, body, { headers: writeHeaders });
  check(res, { 'is 201': (r) => r.status === 201 });
}

// mixed is the realistic shape: many readers polling, occasional writes that
// invalidate the ETag those readers are holding.
//
// The write here attaches a tag rather than inserting a config. Attaching is
// idempotent and still bumps the environment stamp, so the dataset stays the
// same size for the whole run — otherwise the snapshot would grow as the test
// went on and the number would describe the test rather than the service.
export function mixed(data) {
  if (__ITER % 10 === 0) {
    const res = http.post(
      `${BASE}/v1/manage/flags/new_checkout/tags`,
      JSON.stringify({ tag: 'loadtest' }),
      { headers: writeHeaders },
    );
    check(res, { 'tag attached': (r) => r.status === 200 });
    return;
  }
  poll(data);
}
