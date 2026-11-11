// SPDX-License-Identifier: MIT
// Compiled ONLY into the separate pinned Windows experiment variant.
#[path = "owner_native.rs"] mod native;
#[path = "owner_app.rs"] mod app;
#[path = "owner_listeners.rs"] mod listeners;
use native::*;
use std::{os::windows::io::AsRawHandle, process::Child, ptr::null_mut, thread, time::{Duration, Instant}};
type Result<T> = std::result::Result<T, &'static str>;
fn require(value: bool, failure: &'static str) -> Result<()> { if value { Ok(()) } else { Err(failure) } }
pub struct Channel { read: usize, write: usize }
impl Channel {
    pub fn inherited() -> Self {
        fn handle(name: &str) -> usize {
            std::env::var(name).ok().and_then(|v| v.parse::<usize>().ok()).filter(|v| *v != 0 && *v != usize::MAX)
                .expect("OWNED_ATTACH_PRIVATE_CHANNEL_REQUIRED")
        }
        let channel = Self { read: handle("TETHER_OWNER_READ"), write: handle("TETHER_OWNER_WRITE") };
        assert_ne!(channel.read, channel.write, "OWNED_ATTACH_CHANNEL_INVALID");
        // Before spawning Edge. App also uses bInheritHandles=FALSE and filtered environment.
        for value in [channel.read, channel.write] {
            assert_ne!(unsafe { SetHandleInformation(value as Handle, 1, 0) }, 0, "OWNED_ATTACH_CHANNEL_INHERITANCE");
        }
        channel
    }
    fn receive(&self, expected: &str) -> Result<()> {
        let start = Instant::now(); let mut bytes = Vec::new();
        loop {
            require(start.elapsed() < Duration::from_secs(80), "command-timeout")?;
            let mut available = 0;
            require(unsafe { PeekNamedPipe(self.read as Handle, null_mut(), 0, null_mut(), &mut available, null_mut()) } != 0, "command-disconnected")?;
            require(available <= 128, "command-overflow")?;
            if available == 0 { thread::sleep(Duration::from_millis(10)); continue; }
            let mut byte = 0; let mut count = 0;
            require(unsafe { ReadFile(self.read as Handle, &mut byte, 1, &mut count, null_mut()) } != 0 && count == 1, "command-read")?;
            if byte == b'\n' { break; }
            bytes.push(byte); require(bytes.len() < 128, "command-overflow")?;
        }
        require(bytes == expected.as_bytes(), "command-sequence")
    }
    fn send(&self, message: &str) -> Result<()> {
        require(message.len() < 128, "result-overflow")?;
        let text = format!("{message}\n"); let mut count = 0;
        // Exactly three small replies, one outstanding command. The private pipe has 4096
        // bytes of capacity. No unbounded writer loop or public HTTP ownership receipt.
        require(unsafe { WriteFile(self.write as Handle, text.as_ptr(), text.len() as u32, &mut count, null_mut()) } != 0
            && count as usize == text.len(), "result-write")
    }
}
pub fn start(channel: Channel, edge: Child, outer: usize) {
    thread::Builder::new().name("owned-attach-owner".into()).spawn(move || {
        let edge_handle = edge.as_raw_handle();
        let mut application = app::App::empty();
        let mut bindings: Option<listeners::Bindings> = None;
        let outcome = (|| -> Result<()> {
            channel.receive("1 prepare")?;
            // Before first request, launch app under dedicated nested Job.
            application.launch(edge_handle, outer as Handle)?;
            let start = Instant::now();
            bindings = Some(loop {
                app::live(edge_handle)?;
                require(start.elapsed() < Duration::from_secs(15), "listener-ready-timeout")?;
                match listeners::Bindings::capture(application.job, edge_handle) {
                    Ok(binding) => break binding,
                    Err("listener-missing") => thread::sleep(Duration::from_millis(50)),
                    Err(failure) => return Err(failure),
                }
            });
            channel.send("1 ready")?;
            channel.receive("2 attached")?;
            bindings.as_ref().ok_or("listener-lost")?.verify(application.job, edge_handle)?;
            channel.send("2 bound")?;
            channel.receive("3 close")?;
            bindings.as_ref().ok_or("listener-lost")?.verify(application.job, edge_handle)?;
            application.normal_close(edge_handle)?;
            bindings.as_mut().ok_or("listener-lost")?.release()?;
            app::live(edge_handle)?;
            // Immutable normal verdict precedes any cleanup, handle disposal, or driver kill.
            channel.send("3 normal")?;
            Ok(())
        })();
        if let Err(failure) = outcome {
            let app_cleaned = application.cleanup().is_ok();
            let observers_closed = bindings.as_mut().map(|value| value.release().is_ok()).unwrap_or(true)
                && failure != "listener-close";
            let cleanup = if app_cleaned && observers_closed { "cleaned" } else { "cleanup-unconfirmed" };
            let _ = channel.send(&format!("failure {failure} {cleanup}"));
            // Keep original Edge Child alive until outer owner performs cleanup.
        }
        // Successful app/job handles are retained until outer owner exits this process.
        // Failure ownership also remains retained if cleanup was uncertain.
        thread::sleep(Duration::from_secs(120));
        if outcome.is_ok() { let _ = application.cleanup(); }
        drop(edge);
        std::process::exit(1); // lost outer supervisor is never a normal success
    }).expect("OWNED_ATTACH_OWNER_THREAD_FAILED");
}

// Equivalent to upstream tauri's self-assigned kill-on-close Job, but its exact
// handle is retained by this owner for verifying the concrete nested arrangement.
pub fn outer_job() -> usize {
    let job = unsafe { CreateJobObjectW(null_mut(), std::ptr::null()) };
    assert!(!job.is_null(), "OWNED_ATTACH_OUTER_JOB");
    let mut limits = ExtendedLimits::default(); limits.basic.flags = 0x2000;
    assert_ne!(unsafe { SetInformationJobObject(job, 9, (&limits as *const ExtendedLimits).cast(), std::mem::size_of::<ExtendedLimits>() as u32) }, 0, "OWNED_ATTACH_OUTER_LIMIT");
    assert_ne!(unsafe { AssignProcessToJobObject(job, GetCurrentProcess()) }, 0, "OWNED_ATTACH_OUTER_ASSIGN");
    job as usize
}
