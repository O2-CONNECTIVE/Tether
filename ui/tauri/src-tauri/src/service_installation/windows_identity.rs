//! Bounded query-only identity snapshots. No token adjustment or impersonation.
use super::super::{Fault, Principal};
use std::mem::{offset_of, size_of, zeroed};
use std::ptr::null;
use windows_sys::Win32::Foundation::{GetLastError, HANDLE, ERROR_NO_TOKEN, LUID};
use windows_sys::Win32::Security::*;

pub(super) fn wide(value: &str) -> Result<Vec<u16>, Fault> {
    if value.contains('\0') || value.len() > 2048 { return Err(Fault::Denied); }
    Ok(value.encode_utf16().chain(Some(0)).collect())
}
pub(super) fn text(value: &[u16]) -> Result<String, Fault> {
    let n = value.iter().position(|v| *v == 0).ok_or(Fault::Denied)?;
    String::from_utf16(&value[..n]).map_err(|_| Fault::Denied)
}

// Parse only bytes within the returned allocation. Do not call IsValidSid on
// an unchecked pointer: its header/length could lie outside that allocation.
pub(super) fn sid(bytes: &[u8]) -> Result<(String, usize), Fault> {
    if bytes.len() < 8 || bytes[0] != 1 || bytes[1] == 0 || bytes[1] > 15 {
        return Err(Fault::Denied);
    }
    let length = 8 + bytes[1] as usize * 4;
    if bytes.len() < length { return Err(Fault::Denied); }
    let authority = bytes[2..8].iter().fold(0u64, |n, b| (n << 8) | *b as u64);
    let mut value = format!("S-1-{authority}");
    for chunk in bytes[8..length].chunks_exact(4) {
        value.push_str(&format!("-{}", u32::from_le_bytes(chunk.try_into().unwrap())));
    }
    Ok((value, length))
}
fn pointed_sid(pointer: *mut core::ffi::c_void, bytes: &[u8]) -> Result<String, Fault> {
    let offset = (pointer as usize).checked_sub(bytes.as_ptr() as usize).ok_or(Fault::Denied)?;
    sid(bytes.get(offset..).ok_or(Fault::Denied)?).map(|v| v.0)
}
fn luid(value: LUID) -> u64 { ((value.HighPart as u32 as u64) << 32) | value.LowPart as u64 }
fn no_thread_token() -> Result<(), Fault> {
    let mut kind = 0u32;
    let mut returned = 0;
    // Documented current-thread-token pseudo handle; no owned resource created.
    let ok = unsafe { GetTokenInformation((-5isize) as HANDLE, TokenType,
        (&mut kind as *mut u32).cast(), 4, &mut returned) };
    if ok != 0 || unsafe { GetLastError() } != ERROR_NO_TOKEN { return Err(Fault::Denied); }
    Ok(())
}
fn stats(token: HANDLE) -> Result<TOKEN_STATISTICS, Fault> {
    let mut value: TOKEN_STATISTICS = unsafe { zeroed() };
    let mut returned = 0;
    if unsafe { GetTokenInformation(token, TokenStatistics, (&mut value as *mut TOKEN_STATISTICS).cast(),
        size_of::<TOKEN_STATISTICS>() as u32, &mut returned) } == 0
        || returned as usize != size_of::<TOKEN_STATISTICS>() || value.TokenType != TokenPrimary {
        return Err(Fault::Denied);
    }
    Ok(value)
}
fn query(token: HANDLE, class: TOKEN_INFORMATION_CLASS, storage: &mut [usize]) -> Result<&[u8], Fault> {
    let mut returned = 0;
    let capacity = std::mem::size_of_val(storage);
    if unsafe { GetTokenInformation(token, class, storage.as_mut_ptr().cast(), capacity as u32, &mut returned) } == 0
        || returned as usize > capacity { return Err(Fault::Denied); }
    Ok(unsafe { std::slice::from_raw_parts(storage.as_ptr().cast(), returned as usize) })
}
pub(super) fn principal(token: HANDLE) -> Result<Principal, Fault> {
    no_thread_token()?;
    let before = stats(token)?;
    let mut user_storage = [0usize; 256 / size_of::<usize>()];
    let user_bytes = query(token, TokenUser, &mut user_storage)?;
    if user_bytes.len() < size_of::<TOKEN_USER>() { return Err(Fault::Denied); }
    let user = unsafe { std::ptr::read_unaligned(user_bytes.as_ptr().cast::<TOKEN_USER>()) };
    let user = pointed_sid(user.User.Sid, user_bytes)?;
    let mut group_storage = [0usize; 65536 / size_of::<usize>()];
    let bytes = query(token, TokenGroups, &mut group_storage)?;
    let start = offset_of!(TOKEN_GROUPS, Groups);
    if bytes.len() < start { return Err(Fault::Denied); }
    let count = u32::from_le_bytes(bytes[..4].try_into().unwrap()) as usize;
    if count > 1024 || start + count * size_of::<SID_AND_ATTRIBUTES>() > bytes.len() {
        return Err(Fault::Denied);
    }
    let mut groups = Vec::with_capacity(count);
    for i in 0..count {
        let group = unsafe { std::ptr::read_unaligned(bytes.as_ptr().add(start + i * size_of::<SID_AND_ATTRIBUTES>()).cast::<SID_AND_ATTRIBUTES>()) };
        groups.push((pointed_sid(group.Sid, bytes)?, group.Attributes));
    }
    let after = stats(token)?;
    no_thread_token()?;
    if luid(before.TokenId) != luid(after.TokenId)
        || luid(before.AuthenticationId) != luid(after.AuthenticationId)
        || luid(before.ModifiedId) != luid(after.ModifiedId)
        || before.GroupCount != after.GroupCount || count != after.GroupCount as usize {
        return Err(Fault::Denied);
    }
    Ok(Principal { user, kind: after.TokenType as u32, token_id: luid(after.TokenId),
        authentication_id: luid(after.AuthenticationId), modified_id: luid(after.ModifiedId), groups })
}
pub(super) fn service_sid(name: &str) -> Result<String, Fault> {
    if name.is_empty() || name.len() > 128 || !name.bytes().all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'-') {
        return Err(Fault::Denied);
    }
    let account = wide(&format!("NT SERVICE\\{name}"))?;
    let mut storage = [0u32; 17];
    let mut domain = [0u16; 256];
    let (mut sid_length, mut domain_length, mut kind) = (68u32, 256u32, 0);
    if unsafe { LookupAccountNameW(null(), account.as_ptr(), storage.as_mut_ptr().cast(), &mut sid_length,
        domain.as_mut_ptr(), &mut domain_length, &mut kind) } == 0
        || sid_length > 68 || domain_length >= 256 || !(1..=5).contains(&kind)
        || !text(&domain)?.eq_ignore_ascii_case("NT SERVICE") { return Err(Fault::Denied); }
    let bytes = unsafe { std::slice::from_raw_parts(storage.as_ptr().cast(), sid_length as usize) };
    let (value, size) = sid(bytes)?;
    if size != bytes.len() || !value.starts_with("S-1-5-80-") || value.split('-').count() != 9 {
        return Err(Fault::Denied);
    }
    let mut reverse = [0u16; 256];
    domain.fill(0);
    let mut reverse_length = 256;
    domain_length = 256;
    if unsafe { LookupAccountSidW(null(), storage.as_mut_ptr().cast(), reverse.as_mut_ptr(), &mut reverse_length,
        domain.as_mut_ptr(), &mut domain_length, &mut kind) } == 0
        || reverse_length >= 256 || domain_length >= 256 || !(1..=5).contains(&kind)
        || !text(&reverse)?.eq_ignore_ascii_case(name) || !text(&domain)?.eq_ignore_ascii_case("NT SERVICE") {
        return Err(Fault::Denied);
    }
    Ok(value)
}
