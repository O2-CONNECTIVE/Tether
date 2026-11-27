import { decodePairingResponse, decodeReceipt, decodeStatus, type Intent, type Observation,
  type RequestIntent, type ResidentStartup, type StandbyHours } from './model.ts';
import { canRequestPairing, initialResidentState, pairingTimeoutMs, startupWaitMs, type ResidentControllerOptions } from './state.ts';
import { callWithTimeout } from './call-with-timeout.ts';
export { initialResidentState };
export function createResidentController({ invoke, pairingInvoke, onChange, now = () => performance.now(), timeoutMs = 2000, pollMs = 500 }: ResidentControllerOptions) {
  let state = initialResidentState();
  let disposed = false, sealed = false, pageVisible = true;
  let operation: 'status' | 'menu-status' | 'intent' | 'pairing' | null = null, stopRequestedDuringPairing = false;
  let cleanupLatched = false;
  let startup: ResidentStartup | null = null, startupUntil: number | undefined;
  let epoch = 0, requestId = 0, revision = 0, until = 0, lastClock = 0;
  let pending: { id: number; intent: Intent } | undefined;
  let observation: Observation | null = null;
  let poll: ReturnType<typeof setTimeout> | undefined, expiry: ReturnType<typeof setTimeout> | undefined;
  const clock = () => { const t = now(); if (!Number.isFinite(t) || t < lastClock) return NaN; lastClock = t; return t; };
  const publish = (diagnostic: string, cleanupUnknown = false) => {
    if (disposed) return;
    cleanupLatched ||= cleanupUnknown;
    const fresh = !sealed && pageVisible && Number.isFinite(clock()) && lastClock < until;
    const busy = operation !== null;
    const canOff = fresh && !busy && !pending && observation?.canRequestOff === true;
    state = { visible: state.visible, observation: fresh ? observation : null, busy, pending: !!pending,
      canOn: fresh && !busy && !pending && observation?.canRequestOn === true,
      canOff, canExtend: canOff && observation?.standby === 'on' && observation.standbyUntil !== null
        && !observation.standbyExpired && observation.registration !== 'needs_action',
      canPair: canRequestPairing(fresh ? observation : null, !sealed && pageVisible ? startup : null),
      pairingBusy: operation === 'pairing', pairingResult: state.pairingResult,
      cleanupUnknown: cleanupLatched, diagnostic, startup: !sealed && pageVisible ? startup : null,
      lastReceipt: state.lastReceipt, lastRequestIntent: state.lastRequestIntent, lastRequestId: state.lastRequestId };
    onChange(state);
  };
  const stopTimers = () => { clearTimeout(poll); clearTimeout(expiry); };
  const fail = (diagnostic: string, cleanupUnknown = false) => { sealed = true; epoch++; operation = null; stopTimers(); publish(diagnostic, cleanupUnknown); };
  const schedule = (delay = pollMs) => { if (!disposed && !sealed && pageVisible && state.visible) poll = setTimeout(() => { void refresh(); }, delay); };
  async function refresh() {
    if (disposed || sealed || !pageVisible || operation || !invoke) return;
    await readStatus('status');
  }
  async function readStatus(read: 'status' | 'menu-status') {
    clearTimeout(poll); operation = read;
    const generation = ++epoch, started = clock();
    const deadline = startupUntil;
    const budget = deadline === undefined ? timeoutMs : Math.min(timeoutMs, deadline - started);
    const expireStartup = () => {
      if (read === 'status') operation = null;
      startup = null; publish('RESIDENT_SOURCE_UNAVAILABLE');
    };
    if (budget <= 0) return expireStartup();
    publish('RESIDENT_CHECKING');
    try {
      const value = await callWithTimeout(() => invoke?.('get_resident_status', { request: { protocolVersion: 1 } }), budget);
      if (disposed || sealed || generation !== epoch) return;
      const received = clock(), decoded = decodeStatus(value);
      if (deadline !== undefined && received >= deadline) return expireStartup();
      if (!decoded || !Number.isFinite(started) || !Number.isFinite(received) || received - started >= timeoutMs) return fail('RESIDENT_INVALID_RESPONSE');
      if (read === 'status') operation = null;
      if (decoded.source === 'unsupported') {
        if (state.visible) return fail('RESIDENT_SOURCE_CHANGED');
        publish('RESIDENT_UNSUPPORTED'); return;
      }
      if (decoded.startup !== 'checking') startupUntil = undefined;
      state.visible = true;
      const next = decoded.observation;
      if (!next) {
        if (revision !== 0) return fail('RESIDENT_SOURCE_LOST');
        observation = null; startup = decoded.startup ?? null;
        if (startup === 'checking') {
          startupUntil ??= started + startupWaitMs;
          if (received < startupUntil) {
            publish('RESIDENT_STARTUP_CHECKING');
            if (read === 'status') schedule(Math.min(pollMs, startupUntil - received));
            return;
          }
          return expireStartup();
        }
        publish(startup === 'policy-rejected' ? 'RESIDENT_ENROLLMENT_POLICY_REJECTED'
          : startup === 'host-key-unavailable' ? 'RESIDENT_HOST_KEY_UNAVAILABLE' : 'RESIDENT_SOURCE_UNAVAILABLE');
        return;
      }
      startup = null;
      if (next.revision < revision) return fail('RESIDENT_REVISION_INVALID');
      if (next.revision === revision && observation) {
        // Rust withdraws both actions on EOF without advancing its last observation's revision.
        const expired = next.validForMs === 0 && !next.canRequestOn && !next.canRequestOff;
        const previous = expired ? { ...observation, canRequestOn: false, canRequestOff: false } : observation;
        if (JSON.stringify({ ...next, validForMs: 0 }) !== JSON.stringify({ ...previous, validForMs: 0 })) return fail('RESIDENT_REVISION_INVALID');
      }
      if (next.cleanup === 'unknown') return fail('RESIDENT_CLEANUP_UNKNOWN', true);
      const observedUntil = started + next.validForMs;
      if (received >= observedUntil) return fail('RESIDENT_OBSERVATION_EXPIRED');
      revision = next.revision; observation = next;
      const t = next.transition;
      if (t) requestId = Math.max(requestId, t.requestId);
      if (t?.outcome === 'pending') {
        if (pending && (pending.id !== t.requestId || pending.intent !== t.intent)) return fail('RESIDENT_INTENT_UNCONFIRMED');
        pending ??= { id: t.requestId, intent: t.intent };
      }
      // The Go mapper deliberately withholds ON while initial authentication is pending.
      // This exact nonterminal shape grants no action and cannot excuse cleanup uncertainty.
      const pendingOn = t?.intent === 'on' && t.outcome === 'pending' && pending?.id === t.requestId
        && next.registration === 'connecting' && next.work === 'idle' && next.cleanup === 'confirmed'
        && !next.canRequestOn && !next.canRequestOff;
      if ((next.standby === 'unknown' && !pendingOn) || t?.outcome === 'unknown') return fail('RESIDENT_STATE_UNKNOWN');
      if (pending && t?.requestId === pending.id && t.intent === pending.intent && (t.outcome === 'applied' || t.outcome === 'rejected')) pending = undefined;
      if (read === 'menu-status') return { observation: next, until: observedUntil };
      until = observedUntil;
      clearTimeout(expiry);
      expiry = setTimeout(() => fail('RESIDENT_OBSERVATION_EXPIRED'), Math.max(0, until - received));
      publish(pending ? 'RESIDENT_INTENT_PENDING' : 'RESIDENT_OBSERVED'); schedule();
    } catch {
      if (disposed || sealed || generation !== epoch) return;
      if (deadline !== undefined && clock() >= deadline) return expireStartup();
      fail('RESIDENT_RESPONSE_UNCONFIRMED');
    }
  }
  async function request(intent: RequestIntent, standbyHours?: StandbyHours) {
    if (disposed || sealed || !pageVisible || operation || pending || !observation || !invoke) return;
    const t = clock(), observedUntil = until;
    if (!Number.isFinite(t) || t >= observedUntil) return fail('RESIDENT_OBSERVATION_EXPIRED');
    const canRequest = { on: state.canOn, off: state.canOff, extend: state.canExtend };
    if (!canRequest[intent] || (intent === 'extend' && standbyHours === undefined)) return;
    await submitIntent(intent, standbyHours, t, observedUntil);
  }
  async function requestPairing() {
    if (disposed || sealed || !pageVisible || operation || !state.canPair || !pairingInvoke) return;
    operation = 'pairing'; until = 0; state.pairingResult = null; stopTimers(); publish('RESIDENT_PAIRING');
    const result = (await callWithTimeout(() => pairingInvoke(), pairingTimeoutMs)
      .then(decodePairingResponse).catch(() => undefined)) ?? 'unavailable';
    if (disposed || sealed) return;
    operation = null; state.pairingResult = result; publish('RESIDENT_PAIRING_RESULT');
    const stopRequested = stopRequestedDuringPairing; stopRequestedDuringPairing = false;
    await (result === 'succeeded' || stopRequested ? stopFromMenu() : refresh());
  }
  async function submitIntent(intent: RequestIntent, standbyHours: StandbyHours | undefined, t: number, observedUntil: number) {
    if (requestId >= Number.MAX_SAFE_INTEGER) return fail('RESIDENT_REQUEST_EXHAUSTED');
    const id = ++requestId, generation = ++epoch;
    if (intent !== 'extend') pending = { id, intent }; operation = 'intent'; clearTimeout(poll);
    publish(intent === 'extend' ? 'RESIDENT_EXTENDING' : 'RESIDENT_INTENT_PENDING');
    try {
      const receipt = decodeReceipt(await callWithTimeout(() => invoke?.('request_resident_intent', { request: { protocolVersion: 1, requestId: id,
        expectedRevision: revision, intent, ...(standbyHours === undefined || intent === 'off' ? {} : { standbyHours }) } }), timeoutMs), id, intent);
      if (disposed || sealed || generation !== epoch) return;
      const received = clock();
      if (!Number.isFinite(received) || received - t >= timeoutMs || received >= observedUntil) return fail('RESIDENT_INTENT_UNCONFIRMED');
      operation = null;
      if (!receipt || receipt === 'unknown') return fail('RESIDENT_INTENT_UNCONFIRMED');
      state.lastReceipt = receipt; state.lastRequestIntent = intent; state.lastRequestId = id; if (receipt !== 'accepted') pending = undefined;
      if (intent !== 'extend') publish(receipt === 'accepted' ? 'RESIDENT_INTENT_PENDING' : 'RESIDENT_INTENT_REJECTED');
      await refresh();
    } catch { if (!disposed && !sealed && generation === epoch) fail('RESIDENT_INTENT_UNCONFIRMED'); }
  }
  async function stopFromMenu() {
    if (disposed || sealed) return;
    if (operation === 'pairing') { stopRequestedDuringPairing = true; return; }
    if ((operation && operation !== 'status') || !invoke) return;
    const offWasPending = pending?.intent === 'off';
    // Replace an ordinary read and hold the controls through the one-shot menu read.
    until = 0; stopTimers();
    const result = await readStatus('menu-status');
    if (disposed || sealed) return;
    if (!offWasPending && !pending && result?.observation.standby === 'on' && result.observation.canRequestOff) {
      const started = clock();
      if (!Number.isFinite(started) || started >= result.until) return fail('RESIDENT_OBSERVATION_EXPIRED');
      return submitIntent('off', undefined, started, result.until);
    }
    operation = null;
    if (pageVisible) return refresh();
    publish(pending ? 'RESIDENT_INTENT_PENDING' : state.diagnostic);
  }
  async function setPageVisible(visible: boolean) {
    if (disposed || sealed || pageVisible === visible) return;
    pageVisible = visible;
    if (visible) return refresh();
    until = 0; startup = null; stopTimers();
    // Drop the old read, but let the original mutation finish with its own receipt.
    if (operation === 'status') { epoch++; operation = null; }
    publish('RESIDENT_CHECKING');
  }
  return { refresh, request, requestPairing, stopFromMenu, setPageVisible, invalidate: () => fail('RESIDENT_VIEW_INVALIDATED'), dispose: () => {
    disposed = true; sealed = true; epoch++; stopTimers();
  } };
}
