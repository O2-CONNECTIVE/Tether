//! Internal child/IO construction only. No production launch factory exists.
use super::*;
#[derive(Clone, Copy)]
pub(crate) enum Attachment {
    Normal,
    #[cfg(test)]
    Before,
    #[cfg(test)]
    Partial,
    #[cfg(test)]
    Panic,
}
impl Supervisor {
    pub(super) fn start(
        mut command: std::process::Command,
        require_cleanup: bool,
        _fault: Attachment,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        use std::process::Stdio;
        let shared = Arc::new(Mutex::new(Shared::default()));
        let worker_shared = shared.clone();
        let (send, receive) = std::sync::mpsc::sync_channel(1);
        let worker = thread::Builder::new()
            .name("resident-duplex-owner".into())
            .spawn(move || {
                command
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::null());
                let mut child = match command.spawn() {
                    Ok(child) => child,
                    Err(_) => {
                        let _ = send.send(Err(unavailable()));
                        return Ok(End::NotSpawned);
                    }
                };
                // Exact Child stays outside all attachment and pump unwinding.
                let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    #[cfg(test)]
                    if matches!(_fault, Attachment::Before) {
                        return Err(unavailable());
                    }
                    #[cfg(test)]
                    if matches!(_fault, Attachment::Partial) {
                        drop(child.stdout.take());
                        return Err(unavailable());
                    }
                    let pump = Pump::take(&mut child).map_err(|_| unavailable())?;
                    #[cfg(test)]
                    if matches!(_fault, Attachment::Panic) {
                        panic!("synthetic attachment panic");
                    }
                    let clock = ContinuousClock::new().ok_or_else(unavailable)?;
                    let view_clock = ContinuousClock::new().ok_or_else(unavailable)?;
                    let shared = worker_shared.clone();
                    let _ = send.send(Ok(BoundDuplex {
                        shared: shared.clone(),
                        clock: view_clock,
                    }));
                    Ok::<_, io::Error>(drive(pump, clock, shared, &mut child))
                }));
                // Attachment errors/panics close ONLY owned handles, then retain and
                // reap Child. No signal, replacement, or invented cleanup receipt.
                drop(child.stdin.take());
                drop(child.stdout.take());
                let _ = send.send(Err(unavailable()));
                match result {
                    Ok(Ok(exit)) => Ok(End::Reaped(exit)),
                    _ => Ok(End::Reaped(reap(&mut child))),
                }
            })?;
        let source = receive.recv().unwrap_or_else(|_| Err(unavailable()));
        Ok((
            Self {
                shared,
                worker: Some(worker),
                known: false,
                cleanup_confirmed: false,
                startup_rejected: false,
                not_spawned: false,
                require_cleanup,
            },
            source,
        ))
    }
}
