export type Intent = 'on' | 'off';
export type RequestIntent = Intent | 'extend';
export const standbyHoursOptions = [2, 4, 8, 12, 24] as const;
export type StandbyHours = (typeof standbyHoursOptions)[number];
type Transition = { requestId: number; intent: Intent; outcome: 'pending' | 'applied' | 'rejected' | 'unknown' };
type RegistrationReason = 'identity_refused' | 'connection_occupied' | 'gateway_refused';
export type Observation = { revision: number; validForMs: number; standby: 'off' | 'on' | 'stopping' | 'unknown';
  registration: 'disconnected' | 'connecting' | 'reconnecting' | 'authenticated' | 'needs_action' | 'unknown'; caller: string | null;
  registrationReason?: RegistrationReason;
  work: 'idle' | 'preparing' | 'running' | 'stopping' | 'completed' | 'failed' | 'unknown'; cleanup: 'confirmed' | 'pending' | 'unknown';
  canRequestOn: boolean; canRequestOff: boolean; transition: Transition | null;
  standbyUntil: number | null; standbyEndingSoon: boolean; standbyExpired: boolean;
  sleepPrevention: 'inactive' | 'active' | 'unknown' };
export type ResidentStartup = 'checking' | 'policy-rejected';
type ResidentStatus = { source: 'unsupported' } | { source: 'resident-owner'; observation: Observation | null; startup?: ResidentStartup };
export type ResidentInvoke = (command: 'get_resident_status' | 'request_resident_intent', args: { request: {
  protocolVersion: 1; requestId?: number; expectedRevision?: number; intent?: RequestIntent; standbyHours?: StandbyHours } }) => Promise<unknown>;
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const keys = (v: Record<string, unknown>, names: string) => Object.keys(v).sort().join(',') === names.split(',').sort().join(',');
const integer = (v: unknown, min = 0): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v >= min;
const oneOf = <T extends string>(v: unknown, values: readonly T[]): v is T => typeof v === 'string' && values.includes(v as T);
const isIntent = (v: unknown): v is Intent => oneOf(v, ['on', 'off']);
export function decodeStatus(value: unknown): ResidentStatus | undefined {
  if (!record(value) || value.protocolVersion !== 1) return;
  if (value.source === 'unsupported' && keys(value, 'protocolVersion,source')) return { source: 'unsupported' };
  if (value.source !== 'resident-owner') return;
  if (keys(value, 'protocolVersion,source,state')) {
    if (value.state === 'source-unavailable') return { source: 'resident-owner', observation: null };
    if (value.state === 'source-startup-checking' || value.state === 'source-policy-rejected') {
      const startup = value.state === 'source-startup-checking' ? 'checking' : 'policy-rejected';
      return { source: 'resident-owner', observation: null, startup };
    }
  }
  const observationKeys = 'protocolVersion,source,state,revision,validForMs,standby,registration,caller,work,cleanup,canRequestOn,canRequestOff,transition,standbyUntil,standbyEndingSoon,standbyExpired,sleepPrevention';
  const hasRegistrationReason = Object.hasOwn(value, 'registrationReason');
  if (value.state !== 'source-observation' || !keys(value, hasRegistrationReason ? `${observationKeys},registrationReason` : observationKeys)) return;
  const { revision, validForMs, standby, registration, caller, work, cleanup, canRequestOn, canRequestOff,
    standbyUntil, standbyEndingSoon, standbyExpired, sleepPrevention } = value;
  if (!integer(revision, 1) || !integer(validForMs) || validForMs > 3000 || !oneOf(standby, ['off', 'on', 'stopping', 'unknown'])
    || !oneOf(registration, ['disconnected', 'connecting', 'reconnecting', 'authenticated', 'needs_action', 'unknown'])
    || !(caller === null || (typeof caller === 'string' && [...caller].length > 0 && [...caller].length <= 80 && !/\p{Cc}/u.test(caller)))
    || !oneOf(work, ['idle', 'preparing', 'running', 'stopping', 'completed', 'failed', 'unknown'])
    || !oneOf(cleanup, ['confirmed', 'pending', 'unknown']) || typeof canRequestOn !== 'boolean' || typeof canRequestOff !== 'boolean'
    || !(standbyUntil === null || integer(standbyUntil, 1)) || typeof standbyEndingSoon !== 'boolean'
    || (standbyEndingSoon && standbyUntil === null) || typeof standbyExpired !== 'boolean'
    || !oneOf(sleepPrevention, ['inactive', 'active', 'unknown'])) return;
  let registrationReason: RegistrationReason | undefined;
  if (hasRegistrationReason) {
    if (registration !== 'needs_action' || !oneOf(value.registrationReason, ['identity_refused', 'connection_occupied', 'gateway_refused'])) return;
    registrationReason = value.registrationReason;
  } else if (registration === 'needs_action') return;
  if (registration === 'needs_action' && (standby !== 'on' || work !== 'idle' || cleanup !== 'confirmed' || canRequestOn || !canRequestOff)) return;
  let transition: Transition | null = null;
  if (value.transition !== null) {
    const t = value.transition;
    if (!record(t) || !keys(t, 'requestId,intent,outcome') || !integer(t.requestId, 1) || !isIntent(t.intent)
      || !oneOf(t.outcome, ['pending', 'applied', 'rejected', 'unknown'])) return;
    transition = { requestId: t.requestId, intent: t.intent, outcome: t.outcome };
  }
  if ((canRequestOn && canRequestOff) || (validForMs === 0 && (canRequestOn || canRequestOff))
    || (canRequestOn && !(standby === 'off' && work === 'idle' && cleanup === 'confirmed'))
    || (canRequestOff && !(standby === 'on' && cleanup !== 'unknown'))
    || (cleanup === 'unknown' && (canRequestOn || canRequestOff))
    || (transition?.outcome === 'unknown' && (canRequestOn || canRequestOff))
    || (transition?.outcome === 'applied' && (cleanup === 'unknown' || (transition.intent === 'on' ? standby !== 'on'
      : !(standby === 'off' && work === 'idle' && cleanup === 'confirmed'))))) return;
  return { source: 'resident-owner', observation: { revision, validForMs, standby, registration,
    ...(registrationReason ? { registrationReason } : {}), caller, work, cleanup, canRequestOn, canRequestOff, transition,
    standbyUntil, standbyEndingSoon, standbyExpired, sleepPrevention } };
}
export function decodeReceipt(value: unknown, id: number, intent: RequestIntent) {
  if (!record(value) || !keys(value, 'protocolVersion,requestId,intent,disposition') || value.protocolVersion !== 1
    || value.requestId !== id || value.intent !== intent
    || !oneOf(value.disposition, ['unavailable', 'accepted', 'rejected', 'stale', 'unknown'])) return;
  return value.disposition;
}
