export class APIError extends Error { constructor(message:string, readonly status:number) { super(message); } }
export async function api(path:string, csrf:string, body?:unknown) {
  const response = await fetch('/api/v1/admin/'+path, { method: body === undefined ? 'GET' : 'POST', headers: { 'Content-Type':'application/json','X-CSRF-Token':csrf }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  if (!response.ok) throw new APIError(data.error || `Request failed (${response.status})`, response.status);
  return data;
}
export function safeURL(raw:string):string { try {const u=new URL(raw);return u.protocol==='https:' ? u.href : '#';} catch {return '#';} }
