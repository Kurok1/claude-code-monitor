/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

// Shared same-origin JSON fetch helper used by the dashboard and session
// data layers.
export async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, { credentials: 'same-origin' });
  if (!r.ok) {
    const body = await r.text().catch(() => '');
    throw new Error(`GET ${url} → ${r.status}: ${body}`);
  }
  return (await r.json()) as T;
}
