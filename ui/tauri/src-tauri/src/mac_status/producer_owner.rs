//! Exact-child reader ownership prerequisite; production construction remains sealed.
//! No launch factory, source registration or normal startup caller is exposed.
//! Separate native parent owner. Never store this owner in window-managed state.
//! Launch waits for attachment and must also run off the UI thread.
//! Dropping it waits; the eventual native caller must retain/poll it off the UI
//! close path, and keep the parent process alive until join completes. Window
//! close may detach the viewer but MUST NOT exit the parent. No run() caller
//! implements that exit orchestration yet. Neither viewer cancellation nor reaping kills/signals the producer.
#[path = "bound_source.rs"]
mod bound_source;
use super::continuous_clock::ContinuousClock;
use super::native_input::NativeInput;
use super::reader_owner::{JoinState, ReaderOwner};
pub(crate) use bound_source::BoundSource;
use std::fs::File;
use std::io;
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::{mpsc, Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

type Attachment = io::Result<(BoundSource, Arc<Mutex<ReaderOwner>>)>;

#[derive(Debug)]
pub(crate) enum ProducerEnd {
    Reaped(ExitStatus),
    NotSpawned,
}

pub(crate) struct ProducerSupervisor {
    worker: Option<JoinHandle<io::Result<ProducerEnd>>>,
    joined_known: Option<bool>,
}
impl ProducerSupervisor {
    fn start(command: Command) -> io::Result<(Self, io::Result<BoundSource>)> {
        Self::start_with(command, attach)
    }
    fn start_with(
        mut command: Command,
        attach: fn(&mut Child) -> Attachment,
    ) -> io::Result<(Self, io::Result<BoundSource>)> {
        let (send, receive) = mpsc::sync_channel(1);
        // Spawn the supervisor BEFORE the child. Thread creation failure cannot
        // strand a child. All post-spawn failures keep Child in this thread.
        let worker = thread::Builder::new()
            .name("mac-producer-owner".into())
            .spawn(move || {
                command.stdout(Stdio::piped());
                let mut child = match command.spawn() {
                    Ok(child) => child,
                    Err(_) => {
                        let _ = send.send(Err(io::Error::other("MAC_SOURCE_UNAVAILABLE")));
                        return Ok(ProducerEnd::NotSpawned);
                    }
                };
                // Keep exact Child outside attachment unwinding. On failure it
                // is still reaped below, never dropped as a panic side effect.
                let attached =
                    std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| attach(&mut child)))
                        .unwrap_or_else(|_| Err(io::Error::other("MAC_SOURCE_UNAVAILABLE")));
                let reader = match attached {
                    Ok((source, reader)) => {
                        let _ = send.send(Ok(source));
                        Some(reader)
                    }
                    Err(error) => {
                        let _ = send.send(Err(error));
                        None
                    }
                };
                let exit = reap(&mut child);
                // Do NOT cancel on child exit: drain buffered terminal bytes to EOF.
                // Reader has a bounded nonblocking pump; only its own end/cancel joins.
                if let Some(reader) = reader {
                    loop {
                        if matches!(
                            reader.lock().unwrap_or_else(|e| e.into_inner()).poll_join(),
                            JoinState::Joined(_)
                        ) {
                            break;
                        }
                        thread::sleep(Duration::from_millis(20));
                    }
                }
                Ok(ProducerEnd::Reaped(exit))
            })?;
        let source = receive
            .recv()
            .unwrap_or_else(|_| Err(io::Error::other("MAC_SOURCE_UNAVAILABLE")));
        Ok((
            Self {
                worker: Some(worker),
                joined_known: None,
            },
            source,
        ))
    }
    // None means pending and retains the only JoinHandle.
    pub(crate) fn poll_join(&mut self) -> Option<io::Result<ProducerEnd>> {
        if !self.worker.as_ref()?.is_finished() {
            return None;
        }
        let result = self
            .worker
            .take()
            .unwrap()
            .join()
            .unwrap_or_else(|_| Err(io::Error::other("MAC_OWNER_PANICKED")));
        self.joined_known = Some(result.is_ok());
        Some(result)
    }
}
fn attach(child: &mut Child) -> Attachment {
    let unavailable = || io::Error::other("MAC_SOURCE_UNAVAILABLE");
    let clock = ContinuousClock::new().ok_or_else(unavailable)?;
    // This is the stdout of this exact Child, never a supplied fd/path/claim.
    let stdout = child.stdout.take().ok_or_else(unavailable)?;
    let file = File::from(std::os::fd::OwnedFd::from(stdout));
    let input = NativeInput::source(file).ok_or_else(unavailable)?;
    let reader = Arc::new(Mutex::new(ReaderOwner::start(input)?));
    Ok((BoundSource::from_launcher(reader.clone(), clock), reader))
}
fn reap(child: &mut Child) -> ExitStatus {
    loop {
        match child.wait() {
            Ok(status) => return status,
            // Retain Child on wait failure. No timeout, fabricated cleanup or kill.
            Err(_) => thread::sleep(Duration::from_millis(100)),
        }
    }
}
impl Drop for ProducerSupervisor {
    fn drop(&mut self) {
        if let Some(worker) = self.worker.take() {
            self.joined_known = Some(matches!(worker.join(), Ok(Ok(_))));
        }
        wait_known_owner(self.joined_known == Some(true), || {
            thread::park_timeout(Duration::from_secs(1))
        });
    }
}
fn wait_known_owner(known: bool, mut wait: impl FnMut()) {
    if !known {
        loop {
            wait();
        }
    }
}
#[cfg(test)]
#[path = "producer_owner_tests.rs"]
mod tests;
