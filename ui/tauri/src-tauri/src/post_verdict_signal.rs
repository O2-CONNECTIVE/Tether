// Trusted dedicated CI job only. Digests are echoed bindings, independently
// recomputed by the final publisher, not authentication against a same-user writer.
use std::{collections::BTreeMap, fs::{self, OpenOptions}, io::Read, path::{Path, PathBuf}};

fn hex(value: &str, length: usize) -> bool {
    value.len() == length && value.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
fn ordinary(info: &fs::Metadata) -> bool {
    if !info.is_file() || info.len() > 2048 { return false; }
    #[cfg(unix)] {
        use std::os::unix::fs::MetadataExt;
        if info.nlink() != 1 { return false; }
    }
    #[cfg(windows)] {
        use std::os::windows::fs::MetadataExt;
        if info.file_attributes() & 0x400 != 0 { return false; }
    }
    true
}
fn bounded(path: &Path) -> Option<String> {
    if !ordinary(&fs::symlink_metadata(path).ok()?) { return None; }
    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(windows)] {
        use std::os::windows::fs::OpenOptionsExt;
        options.share_mode(0).custom_flags(0x00200000); // OPEN_REPARSE_POINT
    }
    let file = options.open(path).ok()?;
    if !ordinary(&file.metadata().ok()?) { return None; }
    #[cfg(windows)] {
        use std::os::windows::io::AsRawHandle;
        #[link(name = "kernel32")]
        unsafe extern "system" {
            fn GetFileInformationByHandle(handle: *mut std::ffi::c_void, info: *mut [u32; 13]) -> i32;
        }
        let mut info = [0u32; 13];
        // BY_HANDLE_FILE_INFORMATION: 13 DWORDs; nNumberOfLinks is DWORD10.
        if unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut info) } == 0 || info[10] != 1 {
            return None;
        }
    }
    let mut text = String::new();
    file.take(2049).read_to_string(&mut text).ok()?;
    (text.len() <= 2048).then_some(text)
}
// Closed ASCII tokens only: no escapes, whitespace, duplicate keys or JSON coercion.
fn fields(text: &str) -> Option<BTreeMap<&str, &str>> {
    let body = text.strip_prefix('{')?.strip_suffix('}')?;
    let mut result = BTreeMap::new();
    let mut prior = "";
    for pair in body.split(',') {
        let (key, value) = pair.split_once(':')?;
        let key = key.strip_prefix('"')?.strip_suffix('"')?;
        if key <= prior || !key.bytes().all(|b| b.is_ascii_alphanumeric()) { return None; }
        prior = key;
        let value = if value == "1" { value } else {
            value.strip_prefix('"')?.strip_suffix('"')?
        };
        if !value.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-') { return None; }
        result.insert(key, value);
    }
    Some(result)
}

pub struct Signal {
    directory: PathBuf,
    expected: Option<BTreeMap<String, String>>,
    manifest_digest: String,
    attempted: bool,
}
impl Signal {
    pub fn new(directory: &Path, manifest_digest: &str) -> Self {
        let mut result = Self { directory: directory.to_path_buf(), expected: None,
            manifest_digest: manifest_digest.into(), attempted: false };
        let directory_ok = fs::symlink_metadata(directory).map(|m| {
            #[cfg(windows)] {
                use std::os::windows::fs::MetadataExt;
                m.is_dir() && m.file_attributes() & 0x400 == 0
            }
            #[cfg(not(windows))] { m.is_dir() && !m.file_type().is_symlink() }
        }).unwrap_or(false);
        if !directory_ok || !hex(manifest_digest, 64) { return result; }
        let Some(text) = bounded(&directory.join("post-verdict.manifest.json")) else { return result; };
        let Some(values) = fields(&text) else { return result; };
        let keys = ["attempt", "controlCommit", "nonce", "protocol", "run", "sourceCommit", "sourceTree", "specimenSha256", "trial"];
        if values.keys().copied().collect::<Vec<_>>() != keys { return result; }
        // Numeric fields must actually be bare 1, not string "1".
        if !text.starts_with("{\"attempt\":1,") || !text.ends_with(",\"trial\":1}")
            || values["protocol"] != "post-verdict-manifest-1"
            || values["run"].is_empty() || values["run"].len() > 20
            || values["run"].starts_with('0') || !values["run"].bytes().all(|b| b.is_ascii_digit())
            || !text.contains(&format!("\"run\":\"{}\"", values["run"]))
            || !hex(values["nonce"], 64) { return result; }
        for (key, size) in [("controlCommit",40),("sourceCommit",40),("sourceTree",40),("specimenSha256",64)] {
            if !hex(values[key], size) { return result; }
        }
        result.expected = Some(values.into_iter().map(|(k,v)| (k.into(),v.into())).collect());
        result
    }
    pub fn enabled(&self) -> bool { self.expected.is_some() }
    pub fn poll(&mut self) -> Option<(String, String)> {
        let expected = self.expected.as_ref()?;
        if self.attempted { return None; }
        let path = self.directory.join("post-verdict.signal");
        match fs::symlink_metadata(&path) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return None,
            _ => self.attempted = true,
        }
        let text = bounded(&path)?;
        let values = fields(&text)?;
        let keys = ["controlCommit", "failureSha256", "manifestSha256", "protocol", "sourceCommit", "sourceTree", "specimenSha256"];
        if values.keys().copied().collect::<Vec<_>>() != keys
            || values["protocol"] != "post-verdict-signal-1"
            || values["manifestSha256"] != self.manifest_digest
            || !hex(values["failureSha256"], 64) { return None; }
        for key in ["controlCommit", "sourceCommit", "sourceTree", "specimenSha256"] {
            if values[key] != expected[key] { return None; }
        }
        Some((self.manifest_digest.clone(), values["failureSha256"].into()))
    }
}
