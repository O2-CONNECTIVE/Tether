(async () => {
  let stage = 'init', reason = 'unexpected';
  const fail = code => { reason = code; throw new Error('fixture failed'); };
  const emit = async value => {
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      if (typeof window.__TAURI__?.event?.emit === 'function') {
        await window.__TAURI__.event.emit('resident-gui-fixture', value); return;
      }
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    fail('bridge-timeout');
  };
  const phase = async (intent, action, target) => {
    let attempts = 0, previousId = 0, sentRevision = 0;
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      const panel = document.querySelector('#resident-panel');
      const button = document.querySelector(action);
      if (panel) {
        const d = panel.dataset;
        const id = Number(d.residentRequestId), revision = Number(d.residentRevision);
        const receipt = d.residentReceipt;
        if (!Number.isSafeInteger(id) || id < 0 || !Number.isSafeInteger(revision) || revision < 1) {
          fail('identity');
        }
        // Observe the actual allocated ID, which may follow a source watermark.
        // A target button alone can also mean spontaneous OFF, never our success.
        const received = attempts > 0 && id > previousId;
        if (received && receipt === 'accepted' && revision > sentRevision
          && Number(d.residentTransitionId) === id && d.residentTransitionIntent === intent
          && d.residentTransitionOutcome === 'applied' && d.residentStandby === intent
          && document.querySelector(target)?.disabled === false) return;
        if (received && !['accepted', 'stale'].includes(receipt)) fail('receipt');
        if (received && Number(d.residentTransitionId) === id
          && ['rejected', 'unknown'].includes(d.residentTransitionOutcome) && receipt !== 'stale') fail('transition');
        if (button?.disabled === false && (attempts === 0
          || (received && receipt === 'stale' && revision > sentRevision))) {
          if (++attempts > 8) fail('retry-budget');
          previousId = id; sentRevision = revision;
          button.click();
        }
      }
      // Pending/unknown/unavailable and accepted-but-unapplied are never retried.
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    fail('phase-timeout');
  };
  try {
    await emit('initializer-started');
    stage = 'on';
    await phase('on', '#resident-on', '#resident-off');
    stage = 'auth';
    const authenticatedUntil = performance.now() + 20000;
    while (document.querySelector('#resident-panel')?.dataset.residentRegistration !== 'authenticated') {
      if (performance.now() >= authenticatedUntil) fail('auth-timeout');
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    await emit('on-applied');
    stage = 'off';
    await phase('off', '#resident-off', '#resident-on');
    await emit('off-applied');
    stage = 'complete';
    await emit('passed');
  } catch {
    await emit(`failed:${stage}:${reason}`).catch(() => {});
  }
})();
