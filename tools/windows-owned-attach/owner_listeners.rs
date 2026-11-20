// SPDX-License-Identifier: MIT
use super::{native::*, require, Result};
use std::{collections::BTreeSet, ptr::null_mut};

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
struct Listener { family: u32, address: [u8; 16], port: u16, pid: u32 }
fn word(bytes: &[u8], offset: usize) -> u32 {
    u32::from_ne_bytes(bytes[offset..offset + 4].try_into().unwrap())
}
fn snapshot() -> Result<Vec<Listener>> {
    let mut rows = Vec::new();
    for family in [2, 23] {
        let mut size = 0;
        let status = unsafe { GetExtendedTcpTable(null_mut(), &mut size, 0, family, 3, 0) };
        require(status == 122 && (4..=1048576).contains(&size), "listener-table")?;
        let mut data = vec![0u8; size as usize];
        require(unsafe { GetExtendedTcpTable(data.as_mut_ptr().cast(), &mut size, 0, family, 3, 0) } == 0,
            "listener-table")?;
        require(size as usize <= data.len() && size >= 4, "listener-table")?;
        let stride = if family == 2 { 24 } else { 56 };
        let count = word(&data, 0) as usize;
        require(count <= (size as usize - 4) / stride, "listener-table")?;
        for row in data[4..4 + count * stride].chunks_exact(stride) {
            let (port_offset, pid_offset, state_offset) = if family == 2 { (8, 20, 0) } else { (20, 52, 48) };
            let port = u16::from_be_bytes([row[port_offset], row[port_offset + 1]]);
            if ![4444, 4445, 9222].contains(&port) { continue; }
            require(word(row, state_offset) == 2, "listener-state")?;
            let mut address = [0u8; 16];
            let loopback = if family == 2 {
                address[..4].copy_from_slice(&row[4..8]); address[0] == 127
            } else {
                address.copy_from_slice(&row[..16]);
                address == std::net::Ipv6Addr::LOCALHOST.octets() && word(row, 16) == 0
            };
            require(loopback, "listener-nonloopback")?;
            rows.push(Listener { family, address, port, pid: word(row, pid_offset) });
        }
    }
    Ok(rows)
}
// Only debugger listener discovery uses PID. A query/synchronize handle is retained and
// Job membership plus liveness is established before accepting the endpoint. Roles never
// come from PID/name/parent data. Socket ownership is sampled, not an adversarial guarantee.
pub struct Bindings { rows: Vec<Listener>, browsers: Vec<(u32, Handle)>, released: Option<bool> }
impl Bindings {
    pub fn capture(job: Handle, edge: Handle) -> Result<Self> {
        let rows = snapshot()?;
        // Readiness polling is allowed only before any candidate handle is captured.
        for port in [4444, 4445, 9222] {
            require(rows.iter().any(|row| row.port == port), "listener-missing")?;
        }
        let mut result = Self { rows, browsers: Vec::new(), released: None };
        for pid in result.rows.iter().filter(|r| r.port == 9222).map(|r| r.pid).collect::<BTreeSet<_>>() {
            let handle = unsafe { OpenProcess(0x101000, 0, pid) };
            if handle.is_null() { result.release()?; return Err("listener-open"); }
            result.browsers.push((pid, handle));
        }
        if let Err(failure) = result.verify(job, edge) { result.release()?; return Err(failure); }
        Ok(result)
    }
    pub fn verify(&self, job: Handle, edge: Handle) -> Result<()> {
        let current = snapshot()?;
        require(!self.browsers.is_empty(), "listener-lost")?;
        require(current.iter().copied().collect::<BTreeSet<_>>() == self.rows.iter().copied().collect(), "listener-changed")?;
        let self_pid = std::process::id();
        let edge_pid = unsafe { GetProcessId(edge) };
        for port in [4444, 4445, 9222] {
            require(current.iter().any(|r| r.port == port), "listener-lost")?;
        }
        for row in &current {
            require(match row.port {
                4444 => row.pid == self_pid,
                4445 => row.pid == edge_pid,
                _ => self.browsers.iter().any(|(pid, _)| *pid == row.pid),
            }, "listener-role")?;
        }
        for (pid, handle) in &self.browsers {
            let mut member = 0;
            require(unsafe { WaitForSingleObject(*handle, 0) } == 258 && unsafe { GetProcessId(*handle) } == *pid,
                "listener-exited")?;
            require(unsafe { IsProcessInJob(*handle, job, &mut member) } != 0 && member != 0,
                "listener-outside-app-job")?;
        }
        require(unsafe { WaitForSingleObject(edge, 0) } == 258, "edge-exited")
    }
    pub fn release(&mut self) -> Result<()> {
        if let Some(closed) = self.released { return require(closed, "listener-close"); }
        let mut closed = true;
        for (_, handle) in &mut self.browsers {
            if !handle.is_null() {
                if unsafe { CloseHandle(*handle) } != 0 { *handle = null_mut(); }
                else { closed = false; }
            }
        }
        self.released = Some(closed); // Never retry a failed handle close.
        require(closed, "listener-close")
    }
}
