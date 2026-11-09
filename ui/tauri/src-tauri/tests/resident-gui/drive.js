(async () => {
  const emit = async value => {
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      if (typeof window.__TAURI__?.event?.emit === 'function') {
        await window.__TAURI__.event.emit('resident-gui-fixture', value); return;
      }
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error('fixture bridge missing');
  };
  const phase = async (action, target) => {
    let attempts = 0, sentId = 0, sentRevision = 0;
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      if (document.querySelector(target)?.disabled === false) return;
      const panel = document.querySelector('#resident-panel');
      const button = document.querySelector(action);
      if (panel && button?.disabled === false) {
        const id = Number(panel.dataset.residentRequestId);
        const revision = Number(panel.dataset.residentRevision);
        const receipt = panel.dataset.residentReceipt;
        if (sentId === 0 || (id === sentId && ['stale', 'rejected'].includes(receipt)
          && revision > sentRevision)) {
          if (++attempts > 8) throw new Error('fixture retry budget exhausted');
          sentId = id + 1; sentRevision = revision;
          button.click();
        } else if (id === sentId && !['stale', 'rejected', 'accepted'].includes(receipt)) {
          throw new Error('fixture result is not retryable');
        }
      }
      // No enabled action means pending/unknown/unavailable is never retried.
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error('fixture condition missing');
  };
  try {
    await emit('initializer-started');
    await phase('#resident-on', '#resident-off');
    await emit('on-applied');
    await phase('#resident-off', '#resident-on');
    await emit('off-applied');
    await emit('passed');
  } catch {
    await emit('failed').catch(() => {});
  }
})();
