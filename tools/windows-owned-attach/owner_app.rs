// SPDX-License-Identifier: MIT
use super::{native::*, require, Result};
use std::{ffi::OsStr, mem::size_of, os::windows::ffi::OsStrExt, path::Path, ptr::{null, null_mut}, thread, time::{Duration, Instant}};
fn wide(value: &OsStr) -> Vec<u16> { value.encode_wide().chain(Some(0)).collect() }
pub struct App { pub job: Handle, pub root: Process, cleanup_attempted: bool }
impl App {
    pub fn empty() -> Self { Self { job: null_mut(), root: Process::default(), cleanup_attempted: false } }
    pub fn launch(&mut self, edge: Handle, outer: Handle) -> Result<()> {
        let app = std::env::var_os("TETHER_OWNER_APP").ok_or("environment")?;
        let profile = std::env::var_os("TETHER_OWNER_PROFILE").ok_or("environment")?;
        require(Path::new(&app).is_absolute() && Path::new(&app).is_file() && Path::new(&profile).is_absolute(), "environment")?;
        // Parent creates a fresh empty UDF for this invocation. No attach fallback to a shared browser.
        require(std::fs::read_dir(&profile).map_err(|_| "profile")?.next().is_none(), "profile")?;
        self.job = unsafe { CreateJobObjectW(null_mut(), null()) };
        require(!self.job.is_null(), "job-create")?;
        let mut limits = ExtendedLimits::default(); limits.basic.flags = 0x2000;
        require(unsafe { SetInformationJobObject(self.job, 9, (&limits as *const ExtendedLimits).cast(), size_of::<ExtendedLimits>() as u32) } != 0, "job-limit")?;
        let mut member = 0;
        require(unsafe { IsProcessInJob(edge, self.job, &mut member) } != 0 && member == 0, "edge-in-app-job")?;
        let mut vars: Vec<_> = std::env::vars_os().filter(|(key, _)| {
            let key = key.to_string_lossy().to_ascii_uppercase();
            !key.starts_with("TETHER_OWNER_") && !key.starts_with("WEBVIEW2_")
        }).collect();
        vars.push(("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS".into(), "--remote-debugging-port=9222 --remote-debugging-address=127.0.0.1".into()));
        vars.push(("WEBVIEW2_USER_DATA_FOLDER".into(), profile));
        vars.sort_by_key(|(key, _)| key.to_string_lossy().to_ascii_uppercase());
        let mut environment = Vec::new();
        for (key, value) in vars { let mut entry = key; entry.push("="); entry.push(value); environment.extend(wide(&entry)); }
        environment.push(0);
        let image = wide(&app);
        let mut command = wide(&OsStr::new(&format!("\"{}\"", app.to_str().ok_or("environment")?)));
        let cwd = wide(Path::new(&app).parent().ok_or("environment")?.as_os_str());
        let startup = Startup { cb: size_of::<Startup>() as u32, ..Startup::default() };
        require(unsafe { CreateProcessW(image.as_ptr(), command.as_mut_ptr(), null_mut(), null_mut(), 0,
            0x404, environment.as_mut_ptr().cast(), cwd.as_ptr(), &startup, &mut self.root) } != 0, "app-create")?;
        require(unsafe { AssignProcessToJobObject(self.job, self.root.process) } != 0, "app-assign")?;
        require(unsafe { IsProcessInJob(self.root.process, self.job, &mut member) } != 0 && member != 0, "app-membership")?;
        require(unsafe { IsProcessInJob(self.root.process, outer, &mut member) } != 0 && member != 0, "app-outer-membership")?;
        require(unsafe { IsProcessInJob(edge, outer, &mut member) } != 0 && member != 0, "edge-outer-membership")?;
        require(unsafe { WaitForSingleObject(self.root.process, 0) } == 258, "app-exited")?;
        require(unsafe { ResumeThread(self.root.thread) } == 1, "app-resume")
    }
    pub fn active(&self) -> Result<u32> {
        let mut accounting = Accounting::default();
        require(unsafe { QueryInformationJobObject(self.job, 1, (&mut accounting as *mut Accounting).cast(), size_of::<Accounting>() as u32, null_mut()) } != 0, "job-query")?;
        Ok(accounting.active)
    }
    pub fn normal_close(&self, edge: Handle) -> Result<()> {
        live(edge)?;
        require(unsafe { WaitForSingleObject(self.root.process, 0) } == 258, "app-exited-before-close")?;
        let started = Instant::now();
        let mut windows = Windows { pid: self.root.pid, found: Vec::new() };
        require(unsafe { EnumWindows(collect_window, (&mut windows as *mut Windows) as isize) } != 0, "window-query")?;
        require(windows.found.len() == 1, "window-ambiguous")?;
        let window = windows.found[0]; let mut pid = 0;
        require(unsafe { GetWindowThreadProcessId(window, &mut pid) } != 0 && pid == self.root.pid
            && unsafe { WaitForSingleObject(self.root.process, 0) } == 258, "window-changed")?;
        require(unsafe { PostMessageW(window, 0x10, 0, 0) } != 0, "window-close")?;
        loop {
            live(edge)?;
            require(started.elapsed() < Duration::from_secs(10), "app-close-timeout")?;
            match unsafe { WaitForSingleObject(self.root.process, 0) } {
                0 => { require(started.elapsed() < Duration::from_secs(10), "app-close-timeout")?; break; }, 258 => thread::sleep(Duration::from_millis(10)), _ => return Err("app-wait"),
            }
        }
        let mut code = 1;
        require(unsafe { GetExitCodeProcess(self.root.process, &mut code) } != 0 && code == 0, "app-exit-nonzero")?;
        let started = Instant::now();
        loop {
            live(edge)?;
            require(started.elapsed() < Duration::from_secs(10), "job-close-timeout")?;
            if self.active()? == 0 { require(started.elapsed() < Duration::from_secs(10), "job-close-timeout")?; break; }
            thread::sleep(Duration::from_millis(10));
        }
        live(edge)
    }
    // Only called after verdict freeze. Do not drop handles if termination is uncertain.
    pub fn cleanup(&mut self) -> Result<()> {
        require(!self.cleanup_attempted, "cleanup-already-attempted")?;
        self.cleanup_attempted = true;
        // Independent attempts: a root failure never suppresses owned Job cleanup.
        let mut accepted = true;
        if !self.root.process.is_null() && unsafe { WaitForSingleObject(self.root.process, 0) } != 0 {
            accepted &= unsafe { TerminateProcess(self.root.process, 77) } != 0;
        }
        if !self.job.is_null() { accepted &= unsafe { TerminateJobObject(self.job, 77) } != 0; }
        let started = Instant::now();
        loop {
            let root_done = self.root.process.is_null() || unsafe { WaitForSingleObject(self.root.process, 0) } == 0;
            let job_done = self.job.is_null() || self.active()? == 0;
            if root_done && job_done { break; }
            require(started.elapsed() < Duration::from_secs(5), "cleanup-timeout")?;
            thread::sleep(Duration::from_millis(10));
        }
        require(accepted, "cleanup-termination-unconfirmed")?;
        let mut closed = true;
        for slot in [&mut self.root.thread, &mut self.root.process, &mut self.job] {
            if !slot.is_null() {
                if unsafe { CloseHandle(*slot) } != 0 { *slot = null_mut(); }
                else { closed = false; }
            }
        }
        // Failed handles remain retained, but cleanup_attempted prohibits retry/reuse.
        require(closed, "cleanup-close")
    }
}
pub fn live(edge: Handle) -> Result<()> { require(unsafe { WaitForSingleObject(edge, 0) } == 258, "edge-exited") }
struct Windows { pid: u32, found: Vec<Handle> }
unsafe extern "system" fn collect_window(window: Handle, value: isize) -> i32 {
    let state = unsafe { &mut *(value as *mut Windows) }; let mut pid = 0;
    if unsafe { GetWindowThreadProcessId(window, &mut pid) } != 0 && pid == state.pid && unsafe { IsWindowVisible(window) } != 0 {
        state.found.push(window);
    }
    1
}
