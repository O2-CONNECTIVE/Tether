//! Real Windows read-only adapter. Source-only candidate; native execution and
//! Windows compilation are separate, deferred evidence. No SCM or pipe calls.
#[path = "windows_identity.rs"]
mod identity;
#[path = "windows_io.rs"]
mod io;
use super::{Ace, Calls, Fault, FileState, Principal};
use std::mem::{size_of, size_of_val, zeroed};
use std::ptr::{null, null_mut};
use std::sync::atomic::AtomicBool;
use std::time::{Duration, Instant};
use windows_sys::Win32::Foundation::{CloseHandle, GetHandleInformation, HANDLE, INVALID_HANDLE_VALUE};
use windows_sys::Win32::Security::{GetKernelObjectSecurity, DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION, TOKEN_QUERY};
use windows_sys::Win32::Storage::FileSystem::*;
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

pub(super) struct Native;
fn flags(handle: HANDLE) -> Result<(), Fault> {
    let mut value = 0;
    if unsafe { GetHandleInformation(handle, &mut value) } == 0 || value != 0 {
        Err(Fault::Denied)
    } else { Ok(()) }
}
impl Calls for Native {
    fn principal(&self) -> Result<Principal, Fault> {
        // Documented current-process-token pseudo handle; never CloseHandle it.
        identity::principal((-4isize) as HANDLE)
    }
    fn token_principal(&self, token: usize) -> Result<Principal, Fault> {
        identity::principal(token as HANDLE)
    }
    fn service_sid(&self, name: &str) -> Result<String, Fault> { identity::service_sid(name) }
    fn open_token(&self) -> Result<usize, Fault> {
        let mut token = null_mut();
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 || token.is_null() {
            return Err(Fault::Denied);
        }
        if flags(token).is_err() {
            if self.close(token as usize).is_err() { self.fatal(); }
            return Err(Fault::Denied);
        }
        Ok(token as usize)
    }
    fn open(&self, path: &str, leaf: bool) -> Result<usize, Fault> {
        let root = path.get(..3).ok_or(Fault::Denied)?;
        if root.as_bytes()[1..] != *b":\\" { return Err(Fault::Denied); }
        let drive = identity::wide(root)?;
        if unsafe { GetDriveTypeW(drive.as_ptr()) } != 3 /* DRIVE_FIXED */ { return Err(Fault::Denied); }
        let name = identity::wide(path)?;
        // READ_CONTROL | FILE_READ_ATTRIBUTES | SYNCHRONIZE; never write,
        // inheritance, delete sharing, backup privilege, or token elevation.
        let access = 0x0002_0000 | FILE_READ_ATTRIBUTES | 0x0010_0000
            | if leaf { FILE_READ_DATA } else { 0 };
        let handle = unsafe { CreateFileW(name.as_ptr(), access, FILE_SHARE_READ, null(), OPEN_EXISTING,
            FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OVERLAPPED, null_mut()) };
        if handle.is_null() || handle == INVALID_HANDLE_VALUE { return Err(Fault::Denied); }
        // Return ownership before any fallible query. The worker records it
        // immediately and inspect checks handle flags along with file evidence.
        Ok(handle as usize)
    }
    fn inspect(&self, file: usize) -> Result<FileState, Fault> { inspect(file as HANDLE) }
    fn read(&self, file: usize, length: usize, stop: &AtomicBool, deadline: Instant, cleanup: Duration)
        -> Result<Vec<u8>, Fault> {
        io::read(self, file as HANDLE, length, stop, deadline, cleanup)
    }
    fn close(&self, handle: usize) -> Result<(), Fault> {
        if unsafe { CloseHandle(handle as HANDLE) } == 0 { Err(Fault::Unknown) } else { Ok(()) }
    }
    fn fatal(&self) -> ! {
        // GUI-process fail-stop, not a service stop. No unwind or claim of
        // successful cleanup: unresolved resources remain until process death.
        std::process::abort()
    }
}
fn inspect(handle: HANDLE) -> Result<FileState, Fault> {
    flags(handle)?;
    if unsafe { GetFileType(handle) } != FILE_TYPE_DISK { return Err(Fault::Denied); }
    let mut info: BY_HANDLE_FILE_INFORMATION = unsafe { zeroed() };
    let mut id: FILE_ID_INFO = unsafe { zeroed() };
    if unsafe { GetFileInformationByHandle(handle, &mut info) } == 0
        || unsafe { GetFileInformationByHandleEx(handle, FileIdInfo, (&mut id as *mut FILE_ID_INFO).cast(),
            size_of::<FILE_ID_INFO>() as u32) } == 0 { return Err(Fault::Denied); }
    let mut path = [0u16; 2048];
    let count = unsafe { GetFinalPathNameByHandleW(handle, path.as_mut_ptr(), path.len() as u32, 0) } as usize;
    if count == 0 || count >= path.len() { return Err(Fault::Denied); }
    let final_path = String::from_utf16(&path[..count]).map_err(|_| Fault::Denied)?;
    let path = final_path.strip_prefix("\\\\?\\").ok_or(Fault::Denied)?.to_owned();
    let mut filesystem = [0u16; 64];
    if unsafe { GetVolumeInformationByHandleW(handle, null_mut(), 0, null_mut(), null_mut(), null_mut(),
        filesystem.as_mut_ptr(), filesystem.len() as u32) } == 0 { return Err(Fault::Denied); }
    let (owner, acl, protected) = security(handle)?;
    if info.nFileSizeHigh & 0x8000_0000 != 0 { return Err(Fault::Denied); }
    Ok(FileState { path, owner, filesystem: identity::text(&filesystem)?, volume: id.VolumeSerialNumber,
        id: id.FileId.Identifier, acl, disk: true, directory: info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY != 0,
        reparse: info.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT != 0, dacl: true, protected,
        links: info.nNumberOfLinks, size: ((info.nFileSizeHigh as u64) << 32) | info.nFileSizeLow as u64 })
}
fn u16_at(bytes: &[u8], offset: usize) -> Result<u16, Fault> {
    Ok(u16::from_le_bytes(bytes.get(offset..offset + 2).ok_or(Fault::Denied)?.try_into().unwrap()))
}
fn u32_at(bytes: &[u8], offset: usize) -> Result<u32, Fault> {
    Ok(u32::from_le_bytes(bytes.get(offset..offset + 4).ok_or(Fault::Denied)?.try_into().unwrap()))
}
fn security(handle: HANDLE) -> Result<(String, Vec<Ace>, bool), Fault> {
    // One bounded self-relative snapshot, not pointer-rich LocalAlloc storage.
    // GetKernelObjectSecurity returns the held kernel object's descriptor.
    let mut storage = [0u32; 16 * 1024];
    let mut returned = 0;
    if unsafe { GetKernelObjectSecurity(handle, OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
        storage.as_mut_ptr().cast(), size_of_val(&storage) as u32, &mut returned) } == 0
        || returned < 20 || returned as usize > size_of_val(&storage) { return Err(Fault::Denied); }
    let bytes = unsafe { std::slice::from_raw_parts(storage.as_ptr().cast::<u8>(), returned as usize) };
    parse_security(bytes)
}
fn parse_security(bytes: &[u8]) -> Result<(String, Vec<Ace>, bool), Fault> {
    if bytes.len() < 20 { return Err(Fault::Denied); }
    let control = u16_at(bytes, 2)?;
    if bytes[0] != 1 || control & 0x8004 != 0x8004 { return Err(Fault::Denied); }
    let owner_offset = u32_at(bytes, 4)? as usize;
    let acl_offset = u32_at(bytes, 16)? as usize;
    if owner_offset < 20 || acl_offset < 20 || owner_offset % 4 != 0 || acl_offset % 4 != 0 {
        return Err(Fault::Denied);
    }
    let owner = identity::sid(bytes.get(owner_offset..).ok_or(Fault::Denied)?)?.0;
    let acl = bytes.get(acl_offset..).ok_or(Fault::Denied)?;
    let size = u16_at(acl, 2)? as usize;
    let count = u16_at(acl, 4)? as usize;
    if size < 8 || size > acl.len() || count > 128 || !matches!(acl[0], 2 | 4) { return Err(Fault::Denied); }
    let acl = &acl[..size];
    let mut offset = 8;
    let mut entries = Vec::with_capacity(count);
    for _ in 0..count {
        let entry = acl.get(offset..).ok_or(Fault::Denied)?;
        let length = u16_at(entry, 2)? as usize;
        if length < 16 || length > entry.len() || length % 4 != 0 || entry[0] > 1 { return Err(Fault::Denied); }
        let (sid, sid_length) = identity::sid(&entry[8..length])?;
        if sid_length != length - 8 { return Err(Fault::Denied); }
        entries.push(Ace { kind: entry[0], flags: entry[1], mask: u32_at(entry, 4)?, sid });
        offset += length;
    }
    Ok((owner, entries, control & 0x1000 != 0))
}

#[cfg(test)]
mod security_tests {
    use super::*;
    fn descriptor() -> Vec<u8> {
        let mut bytes = vec![0; 60];
        bytes[0] = 1;
        bytes[2..4].copy_from_slice(&0x8004u16.to_le_bytes());
        bytes[4..8].copy_from_slice(&20u32.to_le_bytes());
        bytes[16..20].copy_from_slice(&32u32.to_le_bytes());
        let sid = [1, 1, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0];
        bytes[20..32].copy_from_slice(&sid);
        bytes[32] = 2;
        bytes[34..36].copy_from_slice(&28u16.to_le_bytes());
        bytes[36..38].copy_from_slice(&1u16.to_le_bytes());
        bytes[42..44].copy_from_slice(&20u16.to_le_bytes());
        bytes[44..48].copy_from_slice(&0x120089u32.to_le_bytes());
        bytes[48..60].copy_from_slice(&sid);
        bytes
    }
    #[test]
    fn bounded_descriptor_preserves_owner_and_ace() {
        let (owner, acl, protected) = parse_security(&descriptor()).unwrap();
        assert_eq!(owner, "S-1-5-18");
        assert_eq!(acl, vec![Ace { kind: 0, flags: 0, mask: 0x120089, sid: owner }]);
        assert!(!protected);
    }
    #[test]
    fn malformed_descriptor_offsets_and_aces_are_refused() {
        for length in 0..60 { assert!(parse_security(&descriptor()[..length]).is_err()); }
        for (offset, value) in [(0, 2), (3, 0), (16, 0), (34, 255), (36, 129), (40, 2), (42, 255), (49, 15)] {
            let mut bytes = descriptor(); bytes[offset] = value;
            assert!(parse_security(&bytes).is_err(), "offset {offset}");
        }
    }
}
