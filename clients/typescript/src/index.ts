export const MAX_REQUEST_BYTES = 64 * 1024;
export const MAX_RECEIPT_BYTES = 4096;

export interface Source { app?: string; version?: string; environment?: string; actor?: string; }
export interface Feedback { title?: string; feedback: string; source?: Source; context?: unknown; }
export interface Receipt { id: string; status: 'accepted'; }
export interface SubmitOptions { idempotencyKey?: string; signal?: AbortSignal; }

/** A response error carries only a status: never an endpoint or server body. */
export class HTTPError extends Error {
  readonly statusCode: number;
  constructor(status: number) { super(`feedback service returned HTTP ${status}`); this.name = 'HTTPError'; this.statusCode = status; }
}
class RequestError extends Error { constructor() { super('feedback request failed'); this.name = 'RequestError'; } }

const states = new WeakMap<Client, { endpoint: string }>();
const encoder = new TextEncoder();

export class Client {
  constructor(endpoint: string) { states.set(this, { endpoint: validateEndpoint(endpoint) }); }

  /** Returns durable acceptance, not confirmation that the downstream issue was delivered. */
  async submit(feedback: Feedback, options: SubmitOptions = {}): Promise<Receipt> {
    const body = serialize(feedback);
    const key = options.idempotencyKey || randomKey();
    validateKey(key);
    const endpoint = states.get(this)?.endpoint;
    if (!endpoint) throw new RequestError();
    for (let attempt = 0; attempt < 3; attempt += 1) {
      throwIfAborted(options.signal);
      const result = await request(endpoint, body, key, options.signal);
      if (result.receipt) return result.receipt;
      if (!result.retry || attempt === 2) throw result.error;
      await wait((attempt + 1) * 100, options.signal);
    }
    throw new RequestError();
  }
}

/** Overlay nonempty fields and deep-clone a replacement context. */
export function prepare(defaults: Feedback, submission: Partial<Feedback>): Feedback {
  const source = { ...(defaults.source ?? {}) };
  for (const key of ['app', 'version', 'environment', 'actor'] as const) {
    const value = submission.source?.[key];
    if (value) source[key] = value;
  }
  const result: Feedback = { ...defaults, source: Object.keys(source).length ? source : undefined, context: clone(defaults.context) };
  if (submission.title) result.title = submission.title;
  if (submission.feedback) result.feedback = submission.feedback;
  if (submission.context !== undefined) result.context = clone(submission.context);
  return result;
}

function clone(value: unknown): unknown {
  if (value === undefined) return undefined;
  validateContext(value);
  try { return structuredClone(value); } catch { throw new Error('context is not valid JSON'); }
}

function validateEndpoint(input: string): string {
  let url: URL;
  try { url = new URL(input); } catch { throw new Error('invalid feedback endpoint'); }
  const local = ['localhost', '127.0.0.1', '[::1]', '::1'].includes(url.hostname);
  if (!url.hostname || url.username || url.password || url.search || url.hash || !url.pathname.startsWith('/api/v1/feedback/')) throw new Error('invalid feedback endpoint');
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && local)) throw new Error('feedback endpoint must use HTTPS (HTTP is allowed only on loopback)');
  return url.href;
}

function serialize(feedback: Feedback): Uint8Array {
  if (typeof feedback.feedback !== 'string' || feedback.feedback.trim() === '') throw new Error('feedback is required');
  if (feedback.title !== undefined && (typeof feedback.title !== 'string' || /\r|\n/.test(feedback.title) || Array.from(feedback.title).length > 200)) throw new Error('title must be one line of at most 200 characters');
  validateSource(feedback.source);
  if (feedback.context !== undefined) validateContext(feedback.context);
  let json: string | undefined;
  try { json = JSON.stringify(feedback); } catch { throw new Error('context is not valid JSON'); }
  if (json === undefined) throw new Error('context is not valid JSON');
  const bytes = encoder.encode(json);
  if (bytes.byteLength > MAX_REQUEST_BYTES) throw new Error('feedback exceeds limit or is not UTF-8');
  return bytes;
}

function validateSource(source: unknown): void {
  try {
    if (source === undefined) return;
    if (source === null || typeof source !== 'object' || Array.isArray(source)) throw new Error();
    for (const key of ['app', 'version', 'environment', 'actor']) {
      const value = (source as Record<string, unknown>)[key];
      if (value !== undefined && typeof value !== 'string') throw new Error();
    }
  } catch { throw new Error('source must contain strings'); }
}

function validateContext(value: unknown): void {
  try { validateJSON(value, new Set<object>(), 0); } catch { throw new Error('context is not valid JSON'); }
}

function validateJSON(value: unknown, seen: Set<object>, depth: number): void {
  if (depth > 64) throw new Error();
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return;
  if (typeof value === 'number') { if (Number.isFinite(value)) return; throw new Error(); }
  if (typeof value !== 'object' || seen.has(value)) throw new Error();
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null && !Array.isArray(value)) throw new Error();
  seen.add(value);
  for (const item of Array.isArray(value) ? value : Object.values(value)) validateJSON(item, seen, depth + 1);
  seen.delete(value);
}

function validateKey(key: string): void { if (encoder.encode(key).byteLength > 200 || /[^\x20-\x7e]/.test(key)) throw new Error('invalid idempotency key'); }
function randomKey(): string { return Array.from(crypto.getRandomValues(new Uint8Array(24)), (value) => value.toString(16).padStart(2, '0')).join(''); }

async function request(endpoint: string, body: Uint8Array, key: string, signal?: AbortSignal): Promise<{ receipt?: Receipt; error: Error; retry: boolean }> {
  const timeout = AbortSignal.timeout(15_000);
  const attemptSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
  let response: Response;
  try { response = await fetch(endpoint, { method: 'POST', body: body as unknown as BodyInit, headers: { 'content-type': 'application/json', 'idempotency-key': key }, redirect: 'manual', signal: attemptSignal }); }
  catch { if (signal?.aborted) throw cancelled(); return { error: new RequestError(), retry: true }; }
  try {
    const data = await readBounded(response, MAX_RECEIPT_BYTES);
    if (response.status === 202) {
      const receipt = data === undefined ? undefined : parseReceipt(data);
      return receipt ? { receipt, error: new RequestError(), retry: false } : { error: new Error('invalid acceptance receipt'), retry: false };
    }
    return { error: new HTTPError(response.status), retry: response.status === 429 || response.status >= 500 };
  } catch {
    if (signal?.aborted) throw cancelled();
    if (response.status !== 202) return { error: new HTTPError(response.status), retry: response.status === 429 || response.status >= 500 };
    return { error: new RequestError(), retry: true };
  }
}

async function readBounded(response: Response, limit: number): Promise<Uint8Array | undefined> {
  if (!response.body) return new Uint8Array();
  const reader = response.body.getReader(); const chunks: Uint8Array[] = []; let length = 0;
  try {
    for (;;) { const { done, value } = await reader.read(); if (done) break; length += value.byteLength; if (length > limit) { await reader.cancel(); return undefined; } chunks.push(value); }
  } finally { reader.releaseLock(); }
  const output = new Uint8Array(length); let offset = 0;
  for (const chunk of chunks) { output.set(chunk, offset); offset += chunk.byteLength; }
  return output;
}

function parseReceipt(data: Uint8Array): Receipt | undefined {
  let value: unknown;
  try { value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data)); } catch { return undefined; }
  if (!value || typeof value !== 'object') return undefined;
  const receipt = value as Record<string, unknown>;
  return typeof receipt.id === 'string' && receipt.id !== '' && receipt.status === 'accepted' ? { id: receipt.id, status: 'accepted' } : undefined;
}

async function wait(milliseconds: number, signal?: AbortSignal): Promise<void> {
  if (!signal) return new Promise((resolve) => setTimeout(resolve, milliseconds));
  const activeSignal = signal;
  throwIfAborted(activeSignal);
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(done, milliseconds);
    const abort = () => { clearTimeout(timer); activeSignal.removeEventListener('abort', abort); reject(cancelled()); };
    function done(): void { activeSignal.removeEventListener('abort', abort); resolve(); }
    activeSignal.addEventListener('abort', abort, { once: true });
  });
}
function throwIfAborted(signal?: AbortSignal): void { if (signal?.aborted) throw cancelled(); }
function cancelled(): DOMException { return new DOMException('feedback request cancelled', 'AbortError'); }
