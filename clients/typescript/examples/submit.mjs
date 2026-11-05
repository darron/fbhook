import { Client } from '../dist/index.js';

const endpoint = process.env.FBHOOK_ENDPOINT;
const idempotencyKey = process.env.FBHOOK_IDEMPOTENCY_KEY;
if (!endpoint || !idempotencyKey) {
  console.error('FBHOOK_ENDPOINT and FBHOOK_IDEMPOTENCY_KEY are required');
  process.exitCode = 1;
} else {
  try {
    const receipt = await new Client(endpoint).submit({
      feedback: 'Keep search filters when returning to results',
      source: { app: 'fbhook-example', actor: 'operator' },
      context: { page: '/search', sample: true }
    }, { idempotencyKey });
    console.log(JSON.stringify({ id: receipt.id, status: receipt.status }));
  } catch {
    console.error('feedback submission failed');
    process.exitCode = 1;
  }
}
