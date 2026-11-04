import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { inspect } from 'node:util';
import test from 'node:test';

import { Client, HTTPError, prepare } from '../dist/index.js';

async function server(handler) {
  const http = createServer(handler);
  await new Promise((resolve) => http.listen(0, '127.0.0.1', resolve));
  const { port } = http.address();
  return {
    endpoint: `http://127.0.0.1:${port}/api/v1/feedback/secret.endpoint`,
    close: () => new Promise((resolve, reject) => http.close((error) => error ? reject(error) : resolve()))
  };
}

function read(request) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    request.on('data', (chunk) => chunks.push(chunk));
    request.on('end', () => resolve(Buffer.concat(chunks)));
    request.on('error', reject);
  });
}

test('submit returns only a valid durable acceptance receipt', async (t) => {
  const local = await server(async (request, response) => {
    assert.equal(request.headers['content-type'], 'application/json');
    assert.equal(request.headers['idempotency-key'], 'stable-key');
    assert.deepEqual(JSON.parse((await read(request)).toString()), {
      feedback: 'Keep filters', source: { app: 'archive', actor: 'agent' }, context: { page: '/search' }
    });
    response.writeHead(202, { 'content-type': 'application/json' });
    response.end('{"id":"receipt-one","status":"accepted"}');
  });
  t.after(local.close);
  const receipt = await new Client(local.endpoint).submit({
    feedback: 'Keep filters', source: { app: 'archive', actor: 'agent' }, context: { page: '/search' }
  }, { idempotencyKey: 'stable-key' });
  assert.deepEqual(receipt, { id: 'receipt-one', status: 'accepted' });
});

test('retry reuses exact bytes and generated idempotency key after a lost receipt', async (t) => {
  const received = [];
  const local = await server(async (request, response) => {
    received.push({ body: await read(request), key: request.headers['idempotency-key'] });
    if (received.length === 1) { response.writeHead(202, { 'content-length': '40' }); response.write('{"id":"lost","status":"accepted"'); response.destroy(); return; }
    response.writeHead(202); response.end('{"id":"receipt-two","status":"accepted"}');
  });
  t.after(local.close);
  const receipt = await new Client(local.endpoint).submit({ feedback: 'retry me' });
  assert.equal(receipt.id, 'receipt-two');
  assert.equal(received.length, 2);
  assert.deepEqual(received[0].body, received[1].body);
  assert.equal(received[0].key, received[1].key);
  assert.match(received[0].key, /^[a-zA-Z0-9_-]+$/);
});

test('never retries conflicts, invalid acceptance receipts, or redirects', async (t) => {
  let calls = 0;
  const local = await server(async (request, response) => {
    await read(request); calls++;
    response.writeHead(calls === 1 ? 409 : 302, { location: '/api/v1/feedback/other' }); response.end('private response');
  });
  t.after(local.close);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'once' }, { idempotencyKey: 'once' }), HTTPError);
  assert.equal(calls, 1);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'once' }, { idempotencyKey: 'twice' }), HTTPError);
  assert.equal(calls, 2);
});

test('does not retry a conflict whose error body is truncated', async (t) => {
  let calls = 0;
  const local = await server(async (request, response) => {
    await read(request); calls++;
    response.writeHead(409, { 'content-length': '20', connection: 'close' }); response.end('partial');
  });
  t.after(local.close);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'conflict' }), HTTPError);
  assert.equal(calls, 1);
});

test('endpoint and transport errors redact credential URLs from errors and inspection', async () => {
  assert.throws(() => new Client('http://example.test/api/v1/feedback/super-secret'), /must use HTTPS/);
  assert.throws(() => new Client('https://user:pass@example.test/api/v1/feedback/super-secret?leak=yes'), /invalid feedback endpoint/);
  const closed = createServer();
  await new Promise((resolve) => closed.listen(0, '127.0.0.1', resolve));
  const { port } = closed.address();
  await new Promise((resolve, reject) => closed.close((error) => error ? reject(error) : resolve()));
  const client = new Client(`http://127.0.0.1:${port}/api/v1/feedback/super-secret`);
  await assert.rejects(client.submit({ feedback: 'network failure' }), (error) => {
    assert.doesNotMatch(String(error), /super-secret|127\.0\.0\.1/);
    assert.doesNotMatch(inspect(error), /super-secret|127\.0\.0\.1/);
    assert.equal(error.cause, undefined);
    return true;
  });
  assert.doesNotMatch(inspect(client), /super-secret|example\.test/);
});

test('does not retry malformed or oversized 202 receipts', async (t) => {
  let calls = 0;
  const local = await server(async (request, response) => {
    await read(request); calls++;
    response.writeHead(202); response.end(calls === 1 ? '{not json' : 'x'.repeat(4097));
  });
  t.after(local.close);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'bad receipt' }), /invalid acceptance receipt/);
  assert.equal(calls, 1);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'large receipt' }), /invalid acceptance receipt/);
  assert.equal(calls, 2);
});

test('cancellation stops retry backoff without a second request', async (t) => {
  let calls = 0;
  const local = await server(async (request, response) => {
    await read(request); calls++; response.writeHead(503); response.end('x'.repeat(4097));
  });
  t.after(local.close);
  const signal = AbortSignal.timeout(20);
  await assert.rejects(new Client(local.endpoint).submit({ feedback: 'stop' }, { signal }), { name: 'AbortError' });
  assert.equal(calls, 1);
});

test('validates feedback, title, byte limit, and idempotency key before networking', async () => {
  const client = new Client('https://example.test/api/v1/feedback/super-secret');
  await assert.rejects(client.submit({ feedback: ' \n ' }), /feedback is required/);
  await assert.rejects(client.submit({ feedback: 'x', title: 'bad\ntitle' }), /title must be one line/);
  await assert.rejects(client.submit({ feedback: 'x', title: 42 }), /title must be one line/);
  await assert.rejects(client.submit({ feedback: 'x', source: { app: 42 } }), /source must contain strings/);
  await assert.rejects(client.submit({ feedback: 'x', title: '😀'.repeat(201) }), /title must be one line/);
  await assert.rejects(client.submit({ feedback: 'x'.repeat(65537) }), /feedback exceeds limit/);
  await assert.rejects(client.submit({ feedback: 'x' }, { idempotencyKey: 'bad\rkey' }), /invalid idempotency key/);
  await assert.rejects(client.submit({ feedback: 'x' }, { idempotencyKey: 'snowman-☃' }), /invalid idempotency key/);
  await assert.rejects(client.submit({ feedback: 'x', context: { bad: Infinity } }), /context is not valid JSON/);
  const cycle = {}; cycle.self = cycle;
  await assert.rejects(client.submit({ feedback: 'x', context: cycle }), /context is not valid JSON/);
  let deep = {}; for (let i = 0; i < 65; i++) deep = { deep };
  await assert.rejects(client.submit({ feedback: 'x', context: deep }), /context is not valid JSON/);
});

test('an explicitly empty key is replaced before the request is sent', async (t) => {
  const local = await server(async (request, response) => {
    await read(request);
    assert.match(request.headers['idempotency-key'], /^[0-9a-f]{48}$/);
    response.writeHead(202); response.end('{"id":"generated-key","status":"accepted"}');
  });
  t.after(local.close);
  await new Client(local.endpoint).submit({ feedback: 'generated' }, { idempotencyKey: '' });
});

test('prepare overlays nonempty fields and clones replacement context', () => {
  const defaults = { title: 'default title', feedback: 'default feedback', source: { app: 'app', version: '1' }, context: { nested: { value: 1 } } };
  const submission = { feedback: 'provided', source: { actor: 'person' }, context: { nested: { value: 2 } } };
  const result = prepare(defaults, submission);
  assert.deepEqual(result, { title: 'default title', feedback: 'provided', source: { app: 'app', version: '1', actor: 'person' }, context: { nested: { value: 2 } } });
  submission.context.nested.value = 3;
  assert.equal(result.context.nested.value, 2);
});

test('prepare redacts cloning failures from invalid contexts', () => {
  assert.throws(() => prepare({ feedback: 'default' }, { context: () => 'private caller code' }), /context is not valid JSON/);
});
