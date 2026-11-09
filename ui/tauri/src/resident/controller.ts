import { decodeReceipt, decodeStatus, type Intent, type Observation, type ResidentInvoke } from './model.ts';
export type ResidentState = { visible: boolean; observation: Observation | null; busy: boolean; pending: boolean;
  canOn: boolean; canOff: boolean; diagnostic: string;
  lastReceipt: 'accepted' | 'rejected' | 'stale' | 'unavailable' | null; lastRequestId: number };
export const initialResidentState = (): ResidentState => ({ visible: false, observation: null, busy: false, pending: false,
  canOn: false, canOff: false, diagnostic: 'RESIDENT_CHECKING', lastReceipt: null, lastRequestId: 0 });
interface Options { invoke: ResidentInvoke | undefined; onChange: (state: ResidentState) => void; now?: () => number; timeoutMs?: number; pollMs?: number }
export function createResidentController({ invoke, onChange, now = () => performance.now(), timeoutMs = 2000, pollMs = 500 }: Options) {
  let state = initialResidentState();
  let disposed = false, sealed = false, busy = false;
  let epoch = 0, requestId = 0, revision = 0, until = 0, lastClock = 0;
  let pending: { id: number; intent: Intent } | undefined;
  let observation: Observation | null = null;
  let poll: ReturnType<typeof setTimeout> | undefined, expiry: ReturnType<typeof setTimeout> | undefined;
  const clock = () => { const t = now(); if (!Number.isFinite(t) || t < lastClock) return NaN; lastClock = t; return t; };
  const publish = (diagnostic: string) => {
    if (disposed) return;
    const fresh = !sealed && Number.isFinite(clock()) && lastClock < until;
    state = { visible: state.visible, observation: fresh ? observation : null, busy, pending: !!pending,
      canOn: fresh && !busy && !pending && observation?.canRequestOn === true,
      canOff: fresh && !busy && !pending && observation?.canRequestOff === true, diagnostic,
      lastReceipt: state.lastReceipt, lastRequestId: state.lastRequestId };
    onChange(state);
  };
  const stopTimers = () => { clearTimeout(poll); clearTimeout(expiry); };
  const fail = (diagnostic: string) => { sealed = true; epoch++; busy = false; stopTimers(); publish(diagnostic); };
  const call = async (command: Parameters<ResidentInvoke>[0], args: Parameters<ResidentInvoke>[1]) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try { return await Promise.race([invoke?.(command, args), new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error('timeout')), timeoutMs);
    })]); } finally { clearTimeout(timer); }
  };
  const schedule = () => { if (!disposed && !sealed && state.visible) poll = setTimeout(() => { void refresh(); }, pollMs); };
  async function refresh() {
    if (disposed || sealed || busy || !invoke) return;
    clearTimeout(poll); busy = true;
    const generation = ++epoch, started = clock();
    publish('RESIDENT_CHECKING');
    try {
      const value = await call('get_resident_status', { request: { protocolVersion: 1 } });
      if (disposed || sealed || generation !== epoch) return;
      const received = clock(), decoded = decodeStatus(value);
      if (!decoded || !Number.isFinite(started) || !Number.isFinite(received) || received - started >= timeoutMs) return fail('RESIDENT_INVALID_RESPONSE');
      busy = false;
      if (decoded.source === 'unsupported') {
        if (state.visible) return fail('RESIDENT_SOURCE_CHANGED');
        publish('RESIDENT_UNSUPPORTED'); return;
      }
      state.visible = true;
      const next = decoded.observation;
      if (!next) {
        if (revision !== 0) return fail('RESIDENT_SOURCE_LOST');
        observation = null; publish('RESIDENT_SOURCE_UNAVAILABLE'); return;
      }
      if (next.revision < revision || (next.revision === revision && observation
        && JSON.stringify({ ...next, validForMs: 0 }) !== JSON.stringify({ ...observation, validForMs: 0 }))) return fail('RESIDENT_REVISION_INVALID');
      until = started + next.validForMs;
      if (received >= until) return fail('RESIDENT_OBSERVATION_EXPIRED');
      revision = next.revision; observation = next;
      const t = next.transition;
      if (t) requestId = Math.max(requestId, t.requestId);
      if (t?.outcome === 'pending') {
        if (pending && (pending.id !== t.requestId || pending.intent !== t.intent)) return fail('RESIDENT_INTENT_UNCONFIRMED');
        pending ??= { id: t.requestId, intent: t.intent };
      }
      if (next.cleanup === 'unknown' || next.standby === 'unknown' || t?.outcome === 'unknown') return fail('RESIDENT_CLEANUP_UNKNOWN');
      if (pending && t?.requestId === pending.id && t.intent === pending.intent && (t.outcome === 'applied' || t.outcome === 'rejected')) pending = undefined;
      clearTimeout(expiry);
      expiry = setTimeout(() => fail('RESIDENT_OBSERVATION_EXPIRED'), Math.max(0, until - received));
      publish(pending ? 'RESIDENT_INTENT_PENDING' : 'RESIDENT_OBSERVED'); schedule();
    } catch { if (!disposed && !sealed && generation === epoch) fail('RESIDENT_RESPONSE_UNCONFIRMED'); }
  }
  async function request(intent: Intent) {
    if (disposed || sealed || busy || pending || !observation || !invoke) return;
    const t = clock();
    if (!Number.isFinite(t) || t >= until) return fail('RESIDENT_OBSERVATION_EXPIRED');
    if (!(intent === 'on' ? state.canOn : state.canOff)) return;
    if (requestId >= Number.MAX_SAFE_INTEGER) return fail('RESIDENT_REQUEST_EXHAUSTED');
    const id = ++requestId, generation = ++epoch;
    pending = { id, intent }; busy = true; clearTimeout(poll); publish('RESIDENT_INTENT_PENDING');
    try {
      const receipt = decodeReceipt(await call('request_resident_intent', { request: { protocolVersion: 1, requestId: id,
        expectedRevision: revision, intent } }), id, intent);
      if (disposed || sealed || generation !== epoch) return;
      const received = clock();
      if (!Number.isFinite(received) || received - t >= timeoutMs || received >= until) return fail('RESIDENT_INTENT_UNCONFIRMED');
      busy = false;
      if (!receipt || receipt === 'unknown') return fail('RESIDENT_INTENT_UNCONFIRMED');
      state.lastReceipt = receipt; state.lastRequestId = id;
      if (receipt !== 'accepted') pending = undefined;
      publish(receipt === 'accepted' ? 'RESIDENT_INTENT_PENDING' : 'RESIDENT_INTENT_REJECTED');
      await refresh();
    } catch { if (!disposed && !sealed && generation === epoch) fail('RESIDENT_INTENT_UNCONFIRMED'); }
  }
  return { refresh, request, invalidate: () => fail('RESIDENT_VIEW_INVALIDATED'), dispose: () => {
    disposed = true; sealed = true; epoch++; stopTimers();
  } };
}
