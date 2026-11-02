use super::{Ace, FileState, Seed};

pub(super) fn sid(value: &str) -> bool {
    let parts: Vec<_> = value.split('-').collect();
    if !(4..=18).contains(&parts.len()) || parts[0] != "S" || parts[1] != "1" {
        return false;
    }
    parts[2..].iter().enumerate().all(|(i, text)| {
        text.parse::<u64>().is_ok_and(|n| {
            n.to_string() == *text
                && n <= if i == 0 {
                    (1u64 << 48) - 1
                } else {
                    u64::from(u32::MAX)
                }
        })
    })
}
fn path(value: &str) -> bool {
    let bytes = value.as_bytes();
    if bytes.len() <= 3
        || bytes.len() > 1024
        || !bytes[0].is_ascii_uppercase()
        || &bytes[1..3] != b":\\"
    {
        return false;
    }
    let parts: Vec<_> = value[3..].split('\\').collect();
    parts.len() <= 31
        && parts.iter().all(|p| {
            let stem = p.split('.').next().unwrap_or("").to_ascii_uppercase();
            let reserved = matches!(stem.as_str(), "CON" | "PRN" | "AUX" | "NUL")
                || (stem.len() == 4
                    && (stem.starts_with("COM") || stem.starts_with("LPT"))
                    && matches!(stem.as_bytes()[3], b'1'..=b'9'));
            !p.is_empty()
                && *p != "."
                && *p != ".."
                && !p.ends_with('.')
                && !p.ends_with(' ')
                && !reserved
                && !p.chars().any(|c| c < ' ' || "/:<>\"|?*".contains(c))
        })
}
pub(super) fn valid_seed(s: &Seed) -> bool {
    let a = s.record_path.to_ascii_lowercase();
    let b = s.digest_path.to_ascii_lowercase();
    !s.service_name.is_empty()
        && s.service_name.len() <= 128
        && s.service_name
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'-')
        && sid(s.service_sid)
        && s.service_sid.starts_with("S-1-5-80-")
        && s.service_sid.split('-').count() == 9
        && sid(s.operator_sid)
        && sid(s.guest_sid)
        && s.operator_sid != s.guest_sid
        && !trusted(s, s.operator_sid)
        && path(s.record_path)
        && path(s.digest_path)
        && a != b
        && !a.starts_with(&(b.clone() + "\\"))
        && !b.starts_with(&(a + "\\"))
        && s.volume_root.len() == 3
        && s.volume_root.as_bytes()[0].is_ascii_uppercase()
        && &s.volume_root.as_bytes()[1..] == b":\\"
        && s.record_path.starts_with(s.volume_root)
        && s.digest_path.starts_with(s.volume_root)
        && matches!(s.filesystem, "NTFS" | "ReFS")
        && s.serial != 0
        && (1..=120_000).contains(&s.read_ms)
        && (1..=120_000).contains(&s.cleanup_ms)
        && !s.record.is_empty()
        && s.record.len() <= 256 * 1024
        && s.digest.len() == 65
        && s.digest[64] == b'\n'
        && s.digest[..64]
            .iter()
            .all(|b| b.is_ascii_digit() || matches!(b, b'a'..=b'f'))
}
pub(super) fn paths(seed: &Seed) -> Vec<(String, bool)> {
    let mut paths = vec![(seed.volume_root.to_owned(), false)];
    for leaf in [seed.record_path, seed.digest_path] {
        let mut current = seed.volume_root.to_owned();
        for (i, part) in leaf[3..].split('\\').enumerate() {
            if i != 0 {
                current.push('\\');
            }
            current.push_str(part);
            if !paths.iter().any(|(p, _)| p == &current) {
                paths.push((current.clone(), current == leaf));
            }
        }
    }
    paths
}
fn trusted(s: &Seed, principal: &str) -> bool {
    matches!(principal, "S-1-5-18" | "S-1-5-32-544") || principal == s.service_sid
}
pub(super) fn safe_acl(seed: &Seed, acl: &[Ace]) -> bool {
    if acl.len() > 128 {
        return false;
    }
    acl.iter().all(|a| {
        if a.kind > 1 || a.flags & !0x1f != 0 || a.mask & !0xf01f01ff != 0 || !sid(&a.sid) {
            return false;
        }
        if a.kind == 1 || a.flags & 8 != 0 || trusted(seed, &a.sid) {
            return true;
        }
        let mut mask = a.mask & 0x1f01ff;
        for (generic, mapped) in [
            (0x80000000, 0x120089),
            (0x40000000, 0x120116),
            (0x20000000, 0x1200a0),
            (0x10000000, 0x1f01ff),
        ] {
            if a.mask & generic != 0 {
                mask |= mapped;
            }
        }
        mask & 0xd0156 == 0
    })
}
pub(super) fn valid_file(seed: &Seed, f: &FileState, path: &str, leaf: bool) -> bool {
    f.disk
        && !f.reparse
        && f.dacl
        && f.path == path
        && f.volume == seed.serial
        && f.filesystem == seed.filesystem
        && f.id != [0; 16]
        && f.directory != leaf
        && (!leaf || f.links == 1)
        && trusted(seed, &f.owner)
        && safe_acl(seed, &f.acl)
}
