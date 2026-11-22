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
  const phase = async intent => {
    let attempts = 0, previousId = 0, sentRevision = 0;
    const prior = intent === 'on' ? 'off' : 'on';
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      const panel = document.querySelector('#resident-panel');
      const button = document.querySelector('#resident-switch');
      const checked = button?.getAttribute('aria-checked');
      const actionable = button?.disabled === false && button.getAttribute('aria-disabled') !== 'true';
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
          && actionable && checked === String(intent === 'on')) return;
        if (received && !['accepted', 'stale'].includes(receipt)) fail('receipt');
        if (received && Number(d.residentTransitionId) === id
          && ['rejected', 'unknown'].includes(d.residentTransitionOutcome) && receipt !== 'stale') fail('transition');
        if (actionable && d.residentStandby === prior && checked === String(prior === 'on') && (attempts === 0
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
  const waitForAuthentication = async () => {
    stage = 'auth';
    const authenticatedUntil = performance.now() + 20000;
    while (document.querySelector('#resident-panel')?.dataset.residentRegistration !== 'authenticated') {
      if (performance.now() >= authenticatedUntil) fail('auth-timeout');
      await new Promise(resolve => setTimeout(resolve, 50));
    }
  };
  const hideAndReopen = async menuStop => {
    let hidden = false, reopened = false;
    const onId = Number(document.querySelector('#resident-panel')?.dataset.residentRequestId);
    if (!Number.isSafeInteger(onId) || onId < 1) fail('identity');
    const observeVisibility = () => {
      if (!hidden && document.hidden) {
        hidden = true;
        void emit('window-hidden').catch(() => {});
      }
    };
    document.addEventListener('visibilitychange', observeVisibility);
    window.addEventListener('tether-fixture-window-reopened', () => { reopened = true; }, { once: true });
    const observeStopSignal = async () => window.__TAURI__.event.emit('resident-gui-menu-stop', 'stop-received');
    const onStop = () => { void observeStopSignal().catch(() => {}); };
    if (menuStop) window.addEventListener('tether-resident-stop', onStop, { once: true });
    stage = menuStop ? 'menu-stop' : 'reopen';
    try {
      await emit(menuStop ? 'menu-on-applied' : 'on-applied');
      const reopenedUntil = performance.now() + 15000;
      for (;;) {
        const status = document.querySelector('#resident-panel')?.dataset;
        const button = document.querySelector('#resident-switch');
        const matched = menuStop
          ? status?.residentStandby === 'off' && Number(status.residentRequestId) === onId + 1
            && status.residentReceipt === 'accepted' && Number(status.residentTransitionId) === onId + 1
            && status.residentTransitionIntent === 'off' && status.residentTransitionOutcome === 'applied'
          : status?.residentStandby === 'on' && Number(status.residentRequestId) === onId;
        if (hidden && reopened && document.visibilityState === 'visible' && matched
          && Number(status.residentRevision) > 0 && button?.disabled === false
          && button.getAttribute('aria-disabled') !== 'true') break;
        if (performance.now() >= reopenedUntil) fail('phase-timeout');
        await new Promise(resolve => setTimeout(resolve, 50));
      }
    } finally {
      window.removeEventListener('tether-resident-stop', onStop);
      document.removeEventListener('visibilitychange', observeVisibility);
    }
  };
  try {
    await emit('initializer-started');
    stage = 'on';
    await phase('on');
    await waitForAuthentication();
    await hideAndReopen(false);
    stage = 'off';
    await phase('off');
    await emit('off-applied');
    if (window.__tetherFixtureMenuStop === true) {
      stage = 'on';
      await phase('on');
      await waitForAuthentication();
      await hideAndReopen(true);
      await emit('menu-off-applied');
    }
    stage = 'complete';
    await emit('passed');
  } catch {
    await emit(`failed:${stage}:${reason}`).catch(() => {});
  }
})();
