// SPDX-License-Identifier: MIT
// Private Windows x64 declarations for the disposable owned-attach experiment.
use std::ffi::c_void;
pub type Handle = *mut c_void;
#[repr(C)] #[derive(Default)]
pub struct Startup {
    pub cb: u32, pub reserved: Handle, pub desktop: Handle, pub title: Handle,
    pub x: u32, pub y: u32, pub xs: u32, pub ys: u32, pub xc: u32, pub yc: u32,
    pub fill: u32, pub flags: u32, pub show: u16, pub reserved_size: u16,
    pub reserved_bytes: Handle, pub input: Handle, pub output: Handle, pub error: Handle,
}
#[repr(C)] #[derive(Default)]
pub struct Process { pub process: Handle, pub thread: Handle, pub pid: u32, pub tid: u32 }
#[repr(C)] #[derive(Default)]
pub struct BasicLimits {
    pub process_time: i64, pub job_time: i64, pub flags: u32, pub min: usize, pub max: usize,
    pub active: u32, pub affinity: usize, pub priority: u32, pub scheduling: u32,
}
#[repr(C)] #[derive(Default)]
pub struct ExtendedLimits {
    pub basic: BasicLimits, pub io: [u64; 6], pub memory: usize, pub job_memory: usize,
    pub peak: usize, pub peak_job: usize,
}
#[repr(C)] #[derive(Default)]
pub struct Accounting { pub times: [i64; 4], pub faults: u32, pub total: u32, pub active: u32, pub terminated: u32 }
#[link(name="kernel32")]
unsafe extern "system" {
    pub fn GetCurrentProcess() -> Handle;
    pub fn CreateJobObjectW(attributes: Handle, name: *const u16) -> Handle;
    pub fn SetInformationJobObject(job: Handle, kind: i32, info: *const c_void, size: u32) -> i32;
    pub fn QueryInformationJobObject(job: Handle, kind: i32, info: *mut c_void, size: u32, returned: *mut u32) -> i32;
    pub fn CreateProcessW(image: *const u16, command: *mut u16, pa: Handle, ta: Handle, inherit: i32,
        flags: u32, environment: Handle, cwd: *const u16, startup: *const Startup, process: *mut Process) -> i32;
    pub fn AssignProcessToJobObject(job: Handle, process: Handle) -> i32;
    pub fn IsProcessInJob(process: Handle, job: Handle, member: *mut i32) -> i32;
    pub fn ResumeThread(thread: Handle) -> u32;
    pub fn WaitForSingleObject(handle: Handle, timeout: u32) -> u32;
    pub fn GetExitCodeProcess(process: Handle, code: *mut u32) -> i32;
    pub fn TerminateProcess(process: Handle, code: u32) -> i32;
    pub fn TerminateJobObject(job: Handle, code: u32) -> i32;
    pub fn CloseHandle(handle: Handle) -> i32;
    pub fn SetHandleInformation(handle: Handle, mask: u32, flags: u32) -> i32;
    pub fn OpenProcess(access: u32, inherit: i32, pid: u32) -> Handle;
    pub fn GetProcessId(process: Handle) -> u32;
    pub fn PeekNamedPipe(pipe: Handle, buffer: Handle, size: u32, read: *mut u32, available: *mut u32, left: *mut u32) -> i32;
    pub fn ReadFile(file: Handle, buffer: *mut u8, size: u32, read: *mut u32, overlapped: Handle) -> i32;
    pub fn WriteFile(file: Handle, buffer: *const u8, size: u32, written: *mut u32, overlapped: Handle) -> i32;
}
#[link(name="user32")]
unsafe extern "system" {
    pub fn EnumWindows(callback: unsafe extern "system" fn(Handle, isize) -> i32, data: isize) -> i32;
    pub fn GetWindowThreadProcessId(window: Handle, pid: *mut u32) -> u32;
    pub fn IsWindowVisible(window: Handle) -> i32;
    pub fn PostMessageW(window: Handle, message: u32, w: usize, l: isize) -> i32;
}
#[link(name="iphlpapi")]
unsafe extern "system" {
    pub fn GetExtendedTcpTable(table: Handle, size: *mut u32, order: i32, family: u32, kind: i32, reserved: u32) -> u32;
}
