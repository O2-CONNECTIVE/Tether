import type { Observation, PairingInvoke, PairingResult, RequestIntent, ResidentInvoke, ResidentStartup } from './model.ts';

export type ResidentState = { visible: boolean; observation: Observation | null; busy: boolean; pending: boolean;
  canOn: boolean; canOff: boolean; canExtend: boolean; canPair: boolean; pairingBusy: boolean; pairingResult: PairingResult | null;
  cleanupUnknown: boolean; diagnostic: string; startup: ResidentStartup | null;
  lastReceipt: 'accepted' | 'rejected' | 'stale' | 'unavailable' | null; lastRequestIntent: RequestIntent | null; lastRequestId: number };
export const initialResidentState = (): ResidentState => ({ visible: false, observation: null, busy: false, pending: false,
  canOn: false, canOff: false, canExtend: false, canPair: false, pairingBusy: false, pairingResult: null,
  cleanupUnknown: false, diagnostic: 'RESIDENT_CHECKING', startup: null,
  lastReceipt: null, lastRequestIntent: null, lastRequestId: 0 });
export const pairingTimeoutMs = 20 * 60 * 1000 + 5000;
export const startupWaitMs = 3000;
export interface ResidentControllerOptions { invoke: ResidentInvoke | undefined; pairingInvoke?: PairingInvoke; onChange: (state: ResidentState) => void;
  now?: () => number; timeoutMs?: number; pollMs?: number }
export function canRequestPairing(observation: Observation | null, startup: ResidentStartup | null) {
  return startup === 'policy-rejected' || startup === 'host-key-unavailable'
    || observation?.registrationReason === 'identity_refused';
}
