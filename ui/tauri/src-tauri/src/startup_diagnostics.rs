// 診断feature専用。UI hookは固定memoryとtry_lockだけを使い、I/Oやjoinを待たない。
use std::{
    fs::OpenOptions,
    io::Write,
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, AtomicU32, AtomicU64, AtomicUsize, Ordering::SeqCst},
        Arc, Mutex,
    },
    thread,
    time::{Duration, Instant},
};

#[derive(Clone, Copy)]
pub enum Stage {
    SetupEnter,
    BuildStart,
    BuildOk,
    BuildError,
    SetupReturn,
    PageFinished,
    EnvironmentRequest,
    EnvironmentCallbackOk,
    EnvironmentCallbackError,
    EnvironmentWaitOk,
    EnvironmentWaitError,
    ControllerRequest,
    ControllerCallbackOk,
    ControllerCallbackError,
    ControllerWaitOk,
    ControllerWaitError,
}
impl Stage {
    fn name(self) -> &'static str {
        match self {
            Self::SetupEnter => "setup-enter",
            Self::BuildStart => "webview-build-start",
            Self::BuildOk => "webview-build-ok",
            Self::BuildError => "webview-build-error",
            Self::SetupReturn => "setup-return",
            Self::PageFinished => "first-page-load-finished",
            Self::EnvironmentRequest => "environment-request",
            Self::EnvironmentCallbackOk => "environment-callback-ok",
            Self::EnvironmentCallbackError => "environment-callback-error",
            Self::EnvironmentWaitOk => "environment-wait-ok",
            Self::EnvironmentWaitError => "environment-wait-error",
            Self::ControllerRequest => "controller-request",
            Self::ControllerCallbackOk => "controller-callback-ok",
            Self::ControllerCallbackError => "controller-callback-error",
            Self::ControllerWaitOk => "controller-wait-ok",
            Self::ControllerWaitError => "controller-wait-error",
        }
    }
}
#[derive(Clone, Copy)]
struct Event {
    seq: u64,
    stage: Stage,
    elapsed_us: u128,
}
struct Recorder {
    start: Instant,
    events: Mutex<Vec<Event>>,
    attempted: AtomicU64,
    dropped: AtomicU64,
    in_flight: AtomicUsize,
    enabled: AtomicBool,
    page_seen: AtomicBool,
    observed: AtomicU32,
    wry_hook: AtomicBool,
}
struct Flight<'a>(&'a Recorder);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.in_flight.fetch_sub(1, SeqCst);
    }
}
impl Recorder {
    fn memory() -> Arc<Self> {
        Arc::new(Self {
            start: Instant::now(),
            events: Mutex::new(Vec::with_capacity(16)),
            attempted: AtomicU64::new(0),
            dropped: AtomicU64::new(0),
            in_flight: AtomicUsize::new(0),
            enabled: AtomicBool::new(true),
            page_seen: AtomicBool::new(false),
            observed: AtomicU32::new(0),
            wry_hook: AtomicBool::new(false),
        })
    }
    fn record(&self, stage: Stage) {
        self.in_flight.fetch_add(1, SeqCst);
        let _flight = Flight(self);
        if !self.enabled.load(SeqCst) {
            return;
        }
        if matches!(stage, Stage::PageFinished) && self.page_seen.swap(true, SeqCst) {
            return;
        }
        let seq = self.attempted.fetch_add(1, SeqCst) + 1;
        if let Ok(mut events) = self.events.try_lock() {
            if events.len() < 16 {
                events.push(Event {
                    seq,
                    stage,
                    elapsed_us: self.start.elapsed().as_micros(),
                });
                self.observed.fetch_or(1 << stage as u8, SeqCst);
                return;
            }
        }
        self.dropped.fetch_add(1, SeqCst);
    }
    fn complete(&self) -> bool {
        let seen = self.observed.load(SeqCst);
        seen & (1 << Stage::BuildError as u8) != 0
            || (seen & (1 << Stage::SetupReturn as u8) != 0
                && seen & (1 << Stage::PageFinished as u8) != 0)
    }
    fn snapshot(&self, requested: bool) -> String {
        self.enabled.store(false, SeqCst);
        // workerだけの有限settlement待ち。UI hookは待たない。
        for _ in 0..100 {
            if self.in_flight.load(SeqCst) == 0 {
                break;
            }
            thread::sleep(Duration::from_millis(1));
        }
        let mut events = self
            .events
            .try_lock()
            .map(|v| v.clone())
            .unwrap_or_default();
        events.sort_by_key(|event| event.seq);
        let sealed_at_us = self.start.elapsed().as_micros();
        let reason = if self.complete() {
            "complete"
        } else if requested {
            "requested"
        } else {
            "deadline"
        };
        let rows: Vec<_> = events
            .iter()
            .map(|e| {
                format!(
                    "{{\"seq\":{},\"stage\":\"{}\",\"elapsedUs\":{}}}",
                    e.seq,
                    e.stage.name(),
                    e.elapsed_us
                )
            })
            .collect();
        format!(
            "{{\"schema\":1,\"variant\":\"host-startup-stage-2\",\"purpose\":\"diagnostic-hints-only\",\"events\":[{}],\"attempted\":{},\"recorded\":{},\"dropped\":{},\"sealed\":{},\"inFlight\":{},\"wryHook\":{},\"sealReason\":\"{}\",\"sealedAtUs\":{}}}",
            rows.join(","),
            self.attempted.load(SeqCst),
            events.len(),
            self.dropped.load(SeqCst),
            requested && self.in_flight.load(SeqCst) == 0,
            self.in_flight.load(SeqCst),
            self.wry_hook.load(SeqCst),
            reason,
            sealed_at_us
        )
    }
}
#[derive(Clone, Default)]
pub struct Trace(Option<Arc<Recorder>>);
impl Trace {
    pub fn start() -> Self {
        let Some(directory) = std::env::var_os("RESCUE_HOST_STARTUP_TRACE_DIR") else {
            return Self::default();
        };
        let recorder = Recorder::memory();
        let worker = recorder.clone();
        let path = PathBuf::from(directory);
        // 専用の既存・非競合CI tempを前提とする。新directoryは作らない。
        if thread::Builder::new()
            .name("startup-diagnostic".into())
            .spawn(move || {
                let marker = path.join("app-startup.finish");
                let until = Instant::now() + Duration::from_secs(120);
                while !worker.complete() && !marker.is_file() && Instant::now() < until {
                    thread::sleep(Duration::from_millis(50));
                }
                let text = worker.snapshot(worker.complete() || marker.is_file());
                if text.len() <= 2048 {
                    if let Ok(mut file) = OpenOptions::new()
                        .write(true)
                        .create_new(true)
                        .open(path.join("app-startup-stages.json"))
                    {
                        let _ = file.write_all(text.as_bytes());
                    }
                }
            })
            .is_err()
        {
            return Self::default();
        }
        Self(Some(recorder))
    }
    pub fn record(&self, stage: Stage) {
        if let Some(recorder) = &self.0 {
            recorder.record(stage);
        }
    }
}

#[cfg(test)]
#[path = "startup_diagnostics_tests.rs"]
mod tests;
