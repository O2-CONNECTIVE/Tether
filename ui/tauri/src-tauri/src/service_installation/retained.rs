use super::{policy, Calls, Fault, FileState, Principal, Seed};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

struct Held {
    handle: usize,
    path: String,
    leaf: bool,
    before: Option<FileState>,
}

// Raw OS handles deliberately have no destructor. The worker's catch-unwind
// guard calls fatal before leaving this owner after a panic; native read keeps
// its pending allocations independently pinned. No stack unwind closes handles.
pub(super) struct Retained<C: Calls> {
    seed: Seed,
    calls: C,
    principal: Option<Principal>,
    token: Option<usize>,
    held: Vec<Held>,
}
impl<C: Calls> Retained<C> {
    pub fn new(seed: Seed, calls: C) -> Self {
        Self {
            seed,
            calls,
            principal: None,
            token: None,
            held: Vec::new(),
        }
    }
    pub fn fatal(&self) -> ! {
        self.calls.fatal()
    }
    fn live(stop: &AtomicBool, deadline: Instant) -> Result<(), Fault> {
        if stop.load(Ordering::Acquire) || Instant::now() >= deadline {
            Err(Fault::Denied)
        } else {
            Ok(())
        }
    }
    fn identity(&self) -> Result<(), Fault> {
        let current = self.calls.principal()?;
        if Some(&current) != self.principal.as_ref() {
            return Err(Fault::Denied);
        }
        if let Some(token) = self.token {
            if self.calls.token_principal(token)? != current {
                return Err(Fault::Denied);
            }
        }
        Ok(())
    }
    pub fn acquire(&mut self, stop: &AtomicBool) -> Result<(), Fault> {
        let deadline = Instant::now() + Duration::from_millis(self.seed.read_ms);
        Self::live(stop, deadline)?;
        let p = self.calls.principal()?;
        if p.user != self.seed.operator_sid
            || p.user == self.seed.guest_sid
            || p.kind != 1
            || p.token_id == 0
            || p.authentication_id == 0
            || p.modified_id == 0
            || p.groups.len() > 1024
        {
            return Err(Fault::Denied);
        }
        self.principal = Some(p);
        if self.calls.service_sid(self.seed.service_name)? != self.seed.service_sid {
            return Err(Fault::Denied);
        }
        self.identity()?;
        Self::live(stop, deadline)?;
        let token = self.calls.open_token()?;
        if token == 0 {
            return Err(Fault::Denied);
        }
        self.token = Some(token);
        self.identity()?;
        for (path, leaf) in policy::paths(&self.seed) {
            Self::live(stop, deadline)?;
            self.identity()?;
            let handle = self.calls.open(&path, leaf)?;
            if handle == 0 {
                return Err(Fault::Denied);
            }
            // Record ownership before any further fallible call or validation.
            self.held.push(Held {
                handle,
                path: path.clone(),
                leaf,
                before: None,
            });
            let state = self.calls.inspect(handle)?;
            if !policy::valid_file(&self.seed, &state, &path, leaf) {
                return Err(Fault::Denied);
            }
            self.held.last_mut().ok_or(Fault::Unknown)?.before = Some(state);
        }
        let record = self
            .held
            .iter()
            .find(|h| h.path == self.seed.record_path)
            .ok_or(Fault::Denied)?;
        let digest = self
            .held
            .iter()
            .find(|h| h.path == self.seed.digest_path)
            .ok_or(Fault::Denied)?;
        if record.before.as_ref().ok_or(Fault::Denied)?.id
            == digest.before.as_ref().ok_or(Fault::Denied)?.id
        {
            return Err(Fault::Denied);
        }
        for (path, expected) in [
            (self.seed.record_path, self.seed.record),
            (self.seed.digest_path, self.seed.digest),
        ] {
            Self::live(stop, deadline)?;
            let held = self
                .held
                .iter()
                .find(|h| h.path == path)
                .ok_or(Fault::Denied)?;
            if held.before.as_ref().ok_or(Fault::Denied)?.size != expected.len() as u64 {
                return Err(Fault::Denied);
            }
            let bytes = self.calls.read(
                held.handle,
                expected.len(),
                stop,
                deadline,
                Duration::from_millis(self.seed.cleanup_ms),
            )?;
            self.recheck_until(stop, deadline)?;
            if bytes != expected {
                return Err(Fault::Denied);
            }
        }
        self.recheck_until(stop, deadline)
    }
    pub fn recheck(&self, stop: &AtomicBool) -> Result<(), Fault> {
        self.recheck_until(
            stop,
            Instant::now() + Duration::from_millis(self.seed.read_ms),
        )
    }
    fn recheck_until(&self, stop: &AtomicBool, deadline: Instant) -> Result<(), Fault> {
        Self::live(stop, deadline)?;
        self.identity()?;
        for h in &self.held {
            Self::live(stop, deadline)?;
            let state = self.calls.inspect(h.handle)?;
            if !policy::valid_file(&self.seed, &state, &h.path, h.leaf)
                || Some(&state) != h.before.as_ref()
            {
                return Err(Fault::Denied);
            }
        }
        self.identity()?;
        Self::live(stop, deadline)
    }
    pub fn close(&mut self) -> Result<(), Fault> {
        let deadline = Instant::now() + Duration::from_millis(self.seed.cleanup_ms);
        // A failed close is terminal uncertainty. No automatic retry.
        for h in self.held.iter_mut().rev() {
            if h.handle != 0 {
                if Instant::now() >= deadline {
                    return Err(Fault::Unknown);
                }
                self.calls.close(h.handle)?;
                h.handle = 0;
            }
        }
        if let Some(token) = self.token {
            if Instant::now() >= deadline {
                return Err(Fault::Unknown);
            }
            self.calls.close(token)?;
            self.token = None;
        }
        Ok(())
    }
}
