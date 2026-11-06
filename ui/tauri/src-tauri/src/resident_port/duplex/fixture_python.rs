//! Test setup only: discover a developer interpreter without running it.
use std::{
    io::{self, Read},
    path::PathBuf,
    process::{Child, Command, ExitStatus, Stdio},
    thread,
    time::{Duration, Instant},
};

fn reap_lookup(child: &mut Child) {
    loop {
        match child.wait() {
            Ok(_) => return,
            // Keep the original owner on unconfirmed collection, like the
            // fixture Supervisor. A setup failure never becomes success.
            Err(_) => thread::park_timeout(Duration::from_millis(100)),
        }
    }
}

fn wait_lookup(child: &mut Child, budget: Duration) -> io::Result<ExitStatus> {
    let start = Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return Ok(status),
            Ok(None) => {}
            Err(error) => {
                let _ = child.kill();
                reap_lookup(child);
                return Err(error);
            }
        }
        if start.elapsed() >= budget {
            let _ = child.kill();
            reap_lookup(child);
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "fixture tool lookup",
            ));
        }
        thread::sleep(Duration::from_millis(5));
    }
}

pub(super) fn image() -> PathBuf {
    let mut lookup = Command::new("/usr/bin/xcrun");
    lookup.args(["--find", "python3"]).env_clear();
    // This selects a compiler toolchain only, never a production trust root.
    if let Some(developer) = std::env::var_os("DEVELOPER_DIR") {
        lookup.env("DEVELOPER_DIR", developer);
    }
    let mut child = lookup
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("cannot locate fixture Python");
    let status = wait_lookup(&mut child, Duration::from_secs(5))
        .expect("fixture Python lookup did not complete");
    assert!(status.success(), "fixture Python lookup failed");
    let mut bytes = Vec::new();
    child
        .stdout
        .take()
        .unwrap()
        .take(4097)
        .read_to_end(&mut bytes)
        .expect("cannot read fixture Python selection");
    assert!(bytes.len() <= 4096, "fixture Python selection too long");
    let selected = String::from_utf8(bytes).expect("invalid fixture Python path");
    let image = PathBuf::from(selected.trim());
    assert!(image.is_absolute() && image.is_file());
    assert_ne!(image, std::path::Path::new("/usr/bin/python3"));
    image
}

#[test]
fn lookup_timeout_reaps_the_original_child() {
    let mut child = Command::new("/bin/sh")
        .args(["-c", "IFS= read -r line"])
        .env_clear()
        .stdin(Stdio::piped())
        .spawn()
        .unwrap();
    let original = child.id();
    let error = wait_lookup(&mut child, Duration::from_millis(20)).unwrap_err();
    assert_eq!(error.kind(), io::ErrorKind::TimedOut);
    assert_eq!(child.id(), original);
    assert!(!child.try_wait().unwrap().unwrap().success());
}

#[test]
fn lookup_nonzero_exit_is_not_success() {
    let mut child = Command::new("/bin/sh")
        .args(["-c", "exit 7"])
        .env_clear()
        .spawn()
        .unwrap();
    let status = wait_lookup(&mut child, Duration::from_secs(1)).unwrap();
    assert_eq!(status.code(), Some(7));
    assert_eq!(child.try_wait().unwrap(), Some(status));
}
