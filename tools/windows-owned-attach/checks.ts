// Experimental caller copy; existing smoke remains unchanged.
import { createNativeObservation } from '../../ui/tauri/native-smoke/observation.ts';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { Driver } from '../../ui/tauri/native-smoke/webdriver.ts';
import { armRefresh, disarmRefresh, waitForRefresh } from '../../ui/tauri/native-smoke/refresh-observer.ts';
import { checkKeyboard } from '../../ui/tauri/native-smoke/keyboard.ts';
import { checkUnwired } from '../../ui/tauri/native-smoke/assert-unwired.ts';


const output = process.env.OWNED_ATTACH_OUTPUT;
let observation: ReturnType<typeof createNativeObservation> | undefined;
let deadline: ReturnType<typeof setTimeout> | undefined;
try {
  if (process.platform !== 'win32' || !output) throw new Error('NATIVE_ENV_NOT_CONFIGURED');
  observation = createNativeObservation(output);
  deadline = setTimeout(() => {
    try { observation?.fail(new Error('NATIVE_TEST_DEADLINE')); } catch { console.error('NATIVE_EVIDENCE_WRITE_FAILED'); }
    console.error('NATIVE_TEST_DEADLINE'); process.exit(1);
  }, 60000);
  const driver = new Driver('http://127.0.0.1:4444', fetch, value => { observation?.request(value); });
  observation.phase('session-create');
  const prior = JSON.parse(await readFile(path.join(output, 'session.private.json'), 'utf8'));
  if (typeof prior.session !== 'string' || !prior.session || typeof prior.browserVersion !== 'string') throw new Error('OWNED_ATTACH_SESSION_INVALID');
  const tools = JSON.parse(await readFile(new URL('../../ui/tauri/native-smoke/tools.json', import.meta.url), 'utf8'));
  const browserVersion = prior.browserVersion;
  if (browserVersion !== tools.runtimeVersion) throw new Error('OWNED_ATTACH_RUNTIME_MISMATCH');
  driver.session = prior.session;
  observation.phase('origin-check');
  await driver.waitForBundledDocument((origin, state) => { observation?.origin(origin); observation?.documentState(state); });
  observation.phase('readiness');
  const until = Date.now() + 10000;
  let title: unknown;
  do {
    title = await driver.execute("return document.querySelector('#status-title')?.textContent ?? null;");
    observation.title(title);
    if (title && title !== '状態を確認中') break;
    await delay(100);
  } while (Date.now() < until);
  observation.phase('main-screenshot');
  await writeFile(path.join(output, 'native-main.png'), await driver.screenshot());
  observation.phase('unwired-contract');
  const result = await checkUnwired({
    read: () => driver.execute(`return {
      title: document.querySelector('#status-title')?.textContent ?? '',
      startDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='サポートを開始')?.disabled === true,
      stopDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='接続を停止')?.disabled === true,
      permissionValues: [...document.querySelectorAll('.shell-permissions dd')].map(n=>n.textContent),
      diagnostic: document.querySelector('#diagnostic')?.textContent ?? null,
      diagnosticExpanded: document.querySelector('[data-slot="collapsible-trigger"]')?.getAttribute('aria-expanded') === 'true'
    };`),
    openDiagnostics: () => driver.click('[data-slot="collapsible-trigger"]'),
    invoke: () => driver.invoke()
  });
  if (result.checks !== 3) throw new Error('NATIVE_ZERO_OR_INCOMPLETE_CHECKS');
  observation.phase('keyboard');
  const keyboard = await checkKeyboard({
    press: key => driver.press(key),
    read: () => driver.execute(`const active = document.activeElement;
      const trigger = document.querySelector('[data-slot="collapsible-trigger"]');
      const refresh = document.querySelector('#refresh');
      return {
        focused: active === trigger ? 'diagnostics' : active === refresh ? 'refresh' : 'other',
        expanded: trigger?.getAttribute('aria-expanded') === 'true',
        title: document.querySelector('#status-title')?.textContent ?? '',
        startDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='サポートを開始')?.disabled === true,
        stopDisabled: [...document.querySelectorAll('button')].find(b=>b.textContent==='接続を停止')?.disabled === true,
        diagnostic: document.querySelector('#diagnostic')?.textContent ?? null,
        busy: refresh?.getAttribute('aria-busy') === 'true',
        focusVisible: active?.matches(':focus-visible') === true,
        outlineWidth: active ? parseFloat(getComputedStyle(active).outlineWidth) : 0
      };`),
    armRefresh: () => armRefresh(driver),
    disarmRefresh: () => disarmRefresh(driver),
    settle: () => waitForRefresh(driver)
  });
  if (keyboard.cycles !== 3 || keyboard.checks !== 22 || keyboard.refreshes !== 3) throw new Error('NATIVE_KEYBOARD_INCOMPLETE');
  observation.phase('diagnostics-screenshot');
  await driver.execute("document.querySelector('[data-slot=\"collapsible-content\"]')?.scrollIntoView({block:'end',behavior:'instant'}); return true;");
  await writeFile(path.join(output, 'native-diagnostics.png'), await driver.screenshot());
  observation.phase('result-write');
  await writeFile(path.join(output, 'webview-result.json'), JSON.stringify({ phase: 'webview-observed-awaiting-normal-close', nativeTestsStarted: 1, checks: result.checks, browserVersion, actualRustIpc: true, keyboard }));
  // Keep both drivers alive. The supervisor must observe normal close before any session/driver cleanup.
  observation.pass();
  console.log('NATIVE_WEBVIEW_AND_REAL_IPC_OBSERVED');
} catch (error) {
  try { observation?.fail(error); } catch { console.error('NATIVE_EVIDENCE_WRITE_FAILED'); }
  console.error('NATIVE_WEBVIEW_OR_IPC_FAILED');
  process.exitCode = 1;
} finally { clearTimeout(deadline); }
