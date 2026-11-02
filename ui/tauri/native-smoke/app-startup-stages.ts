const stages = ['setup-enter', 'webview-build-start', 'webview-build-ok', 'webview-build-error', 'setup-return', 'first-page-load-finished'] as const;
type Stage = typeof stages[number];
type Event = { seq: number; stage: Stage; elapsedUs: number };
function object(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
function exact(value: Record<string, unknown>, keys: string[]) { return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key)); }
function integer(value: unknown): value is number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0; }
function classifyOriginalStartup(input: unknown) {
  const incomplete = { schema: 1, purpose: 'diagnostic-hints-only', phase: 'diagnostic-incomplete', events: [] as Event[] };
  if (!object(input) || !exact(input, ['schema', 'variant', 'purpose', 'events', 'attempted', 'recorded', 'dropped', 'sealed', 'inFlight']) ||
    input.schema !== 1 || input.variant !== 'host-startup-stage-1' || input.purpose !== 'diagnostic-hints-only' ||
    input.sealed !== true || input.inFlight !== 0 || input.dropped !== 0 || !Array.isArray(input.events) || input.events.length > 6 ||
    input.recorded !== input.events.length || input.attempted !== input.events.length) return incomplete;
  const events: Event[] = []; const seen = new Set<Stage>(); let elapsed = 0;
  for (const item of input.events) {
    if (!object(item) || !exact(item, ['seq', 'stage', 'elapsedUs']) || item.seq !== events.length + 1 ||
      typeof item.stage !== 'string' || !stages.some(stage => stage === item.stage) ||
      !integer(item.elapsedUs) || item.elapsedUs < elapsed) return incomplete;
    const stage = stages.find(stage => stage === item.stage);
    if (!stage || seen.has(stage)) return incomplete;
    if ((events.length === 0 && stage !== 'setup-enter') ||
      (stage === 'webview-build-start' && !seen.has('setup-enter')) ||
      (stage !== 'setup-enter' && stage !== 'webview-build-start' && !seen.has('webview-build-start')) ||
      (stage === 'setup-return' && !seen.has('webview-build-ok')) ||
      (stage === 'webview-build-error' && (seen.has('webview-build-ok') || seen.has('first-page-load-finished'))) ||
      ((stage === 'webview-build-ok' || stage === 'first-page-load-finished') && seen.has('webview-build-error'))) return incomplete;
    events.push({ seq: events.length + 1, stage, elapsedUs: item.elapsedUs }); seen.add(stage); elapsed = item.elapsedUs;
  }
  const phase = seen.has('first-page-load-finished') ? 'first-page-load-finished'
    : seen.has('webview-build-error') ? 'webview-build-error'
    : seen.has('webview-build-ok') ? 'webview-built-page-load-not-observed'
    : seen.has('webview-build-start') ? 'webview-build-not-observed'
    : seen.has('setup-enter') ? 'setup-entered-build-not-observed' : 'setup-not-observed';
  return { schema: 1, purpose: 'diagnostic-hints-only', phase, events };
}


const initializationStages = ['environment-request', 'environment-callback-ok', 'environment-callback-error',
  'environment-wait-ok', 'environment-wait-error', 'controller-request', 'controller-callback-ok',
  'controller-callback-error', 'controller-wait-ok', 'controller-wait-error'] as const;

export function classifyAppStartup(input: unknown) {
  if (!object(input) || input.variant !== 'host-startup-stage-2') return classifyOriginalStartup(input);
  const incomplete = classifyOriginalStartup(null);
  if (!exact(input, ['schema', 'variant', 'purpose', 'events', 'attempted', 'recorded', 'dropped',
    'sealed', 'inFlight', 'wryHook', 'sealReason', 'sealedAtUs']) || input.schema !== 1 ||
    input.purpose !== 'diagnostic-hints-only' || input.sealed !== true || input.inFlight !== 0 ||
    input.dropped !== 0 || typeof input.wryHook !== 'boolean' || !integer(input.sealedAtUs) ||
    (input.sealReason !== 'requested' && input.sealReason !== 'complete') || !Array.isArray(input.events) ||
    input.events.length > 16 || input.attempted !== input.events.length || input.recorded !== input.events.length) return incomplete;
  const seen = new Set<string>(); let elapsed = 0;
  const original: Event[] = [];
  const events: { seq: number; stage: string; elapsedUs: number }[] = [];
  for (const item of input.events) {
    if (!object(item) || !exact(item, ['seq', 'stage', 'elapsedUs']) || item.seq !== events.length + 1 ||
      typeof item.stage !== 'string' || seen.has(item.stage) || !integer(item.elapsedUs) ||
      item.elapsedUs < elapsed || item.elapsedUs > input.sealedAtUs) return incomplete;
    const outer = stages.find(stage => stage === item.stage);
    if (outer) {
      original.push({ seq: original.length + 1, stage: outer, elapsedUs: item.elapsedUs });
    } else {
      if (!input.wryHook || !initializationStages.some(stage => stage === item.stage) ||
        !seen.has('webview-build-start') || seen.has('webview-build-ok') || seen.has('webview-build-error')) return incomplete;
      const prefix = item.stage.startsWith('environment-') ? 'environment' : 'controller';
      const has = (suffix: string) => seen.has(`${prefix}-${suffix}`);
      if (prefix === 'controller' && !seen.has('environment-wait-ok')) return incomplete;
      if (item.stage !== `${prefix}-request` && !has('request')) return incomplete;
      if (has('wait-ok') || has('wait-error')) return incomplete;
      if (item.stage.includes('-callback-') && (has('callback-ok') || has('callback-error'))) return incomplete;
      if (item.stage === `${prefix}-wait-ok` && !has('callback-ok')) return incomplete;
    }
    events.push({ seq: events.length + 1, stage: item.stage, elapsedUs: item.elapsedUs });
    seen.add(item.stage); elapsed = item.elapsedUs;
  }
  const outer = classifyOriginalStartup({ schema: 1, variant: 'host-startup-stage-1', purpose: input.purpose,
    events: original, attempted: original.length, recorded: original.length, dropped: 0, sealed: true, inFlight: 0 });
  if (outer.phase === 'diagnostic-incomplete') return incomplete;
  const complete = seen.has('webview-build-error') || (seen.has('setup-return') && seen.has('first-page-load-finished'));
  if (input.sealReason === 'complete' && !complete) return incomplete;
  if (input.wryHook && (seen.has('webview-build-ok') || seen.has('first-page-load-finished')) && !seen.has('controller-wait-ok')) return incomplete;
  let phase: string = outer.phase;
  if (input.wryHook && phase === 'webview-build-not-observed') {
    phase = seen.has('controller-wait-error') ? 'controller-wait-error'
      : seen.has('controller-wait-ok') ? 'webview-post-controller-build-not-observed'
      : seen.has('controller-callback-error') ? 'controller-callback-error'
      : seen.has('controller-callback-ok') ? 'controller-wait-return-not-observed'
      : seen.has('controller-request') ? 'controller-callback-not-observed'
      : seen.has('environment-wait-error') ? 'environment-wait-error'
      : seen.has('environment-wait-ok') ? 'controller-request-not-observed'
      : seen.has('environment-callback-error') ? 'environment-callback-error'
      : seen.has('environment-callback-ok') ? 'environment-wait-return-not-observed'
      : seen.has('environment-request') ? 'environment-callback-not-observed' : 'environment-request-not-observed';
  }
  return { schema: 1, purpose: 'diagnostic-hints-only', phase, events,
    wryHook: input.wryHook, sealReason: input.sealReason, sealedAtUs: input.sealedAtUs };
}
