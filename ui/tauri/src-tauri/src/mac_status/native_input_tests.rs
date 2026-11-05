use super::*;
use crate::mac_status::reader_owner::{JoinState, ReaderOwner, WorkerEnd};
use crate::mac_status::state::FeedFault;
use std::io::Write;
use std::os::fd::FromRawFd;
use std::time::{Duration, Instant};

unsafe extern "C" {
    fn pipe(descriptors: *mut i32) -> i32;
}
fn owned_pipe() -> (File, File) {
    let mut descriptors = [-1; 2];
    // SAFETY: the two-slot writable buffer is live for this syscall.
    assert_eq!(unsafe { pipe(descriptors.as_mut_ptr()) }, 0);
    // SAFETY: successful pipe returned two new descriptors, each adopted once.
    unsafe {
        (
            File::from_raw_fd(descriptors[0]),
            File::from_raw_fd(descriptors[1]),
        )
    }
}
fn join(owner: &mut ReaderOwner) -> WorkerEnd {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match owner.poll_join() {
            JoinState::Joined(end) => return end,
            JoinState::Pending => {
                assert!(Instant::now() < deadline, "owned fixture join timeout");
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}
fn synthetic_frame(terminal: bool) -> Vec<u8> {
    let millis = (ContinuousClock::new().unwrap().now_seconds().unwrap() * 1000.0).floor() as u64;
    format!(r#"{{"version":1,"source_continuous_ms":{millis},"seq":1,"phase":"{}","reason":"none","grant_remaining_ms":{},"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"{}","terminal_cleanup_unknown":false,"terminal":{terminal}}}
"#,if terminal {"closed"}else{"connecting"},if terminal{0}else{2000},if terminal{"confirmed"}else{"pending"}).into_bytes()
}

#[test]
fn actual_owned_fifo_flags_and_pending_read_are_bounded() {
    let (reader, writer) = owned_pipe();
    let mut input = NativeInput::from_owned_fifo(reader).unwrap();
    let fd = input.file.as_raw_fd();
    // SAFETY: input owns this live descriptor; both calls only read flags.
    assert_ne!(unsafe { fcntl(fd, F_GETFL) } & O_NONBLOCK, 0);
    assert_ne!(unsafe { fcntl(fd, F_GETFD) } & FD_CLOEXEC, 0);
    assert!(matches!(
        input.read_once(&mut [0; 4096]),
        Ok(ReadStep::Pending)
    ));
    drop(writer);
    assert!(matches!(input.read_once(&mut [0; 4096]), Ok(ReadStep::Eof)));
}

#[test]
fn non_fifo_and_wrong_direction_are_rejected() {
    assert!(NativeInput::from_owned_fifo(File::open("/dev/null").unwrap()).is_err());
    let (reader, writer) = owned_pipe();
    assert!(NativeInput::from_owned_fifo(writer).is_err());
    drop(reader);
}

#[test]
fn synthetic_native_fifo_to_validated_snapshot_and_eof_history() {
    for terminal in [false, true] {
        let (reader, mut writer) = owned_pipe();
        let mut owner = ReaderOwner::start(NativeInput::source(reader).unwrap()).unwrap();
        writer.write_all(&synthetic_frame(terminal)).unwrap();
        drop(writer);
        assert_eq!(join(&mut owner), WorkerEnd::SourceEnded);
        let snapshot = owner.snapshot();
        assert_eq!(snapshot.fault, Some(FeedFault::Eof));
        assert_eq!(snapshot.terminal_observed(), terminal);
        assert!(snapshot.frame.is_some());
        assert!(!snapshot.fresh(ContinuousClock::new().unwrap().now_seconds().unwrap()));
    }
}

#[test]
fn cancel_owned_native_fifo_joins_without_closing_writer_or_touching_stdin() {
    let (reader, writer) = owned_pipe();
    let mut owner = ReaderOwner::start(NativeInput::source(reader).unwrap()).unwrap();
    owner.request_stop();
    assert_eq!(join(&mut owner), WorkerEnd::Canceled);
    assert_eq!(owner.snapshot().fault, Some(FeedFault::Stopped));
    assert!(writer.metadata().unwrap().file_type().is_fifo());
}

#[test]
fn partial_native_fifo_eof_is_malformed_not_cleanup_confirmation() {
    let (reader, mut writer) = owned_pipe();
    let mut owner = ReaderOwner::start(NativeInput::source(reader).unwrap()).unwrap();
    writer.write_all(b"{").unwrap();
    drop(writer);
    assert_eq!(join(&mut owner), WorkerEnd::SourceEnded);
    assert_eq!(owner.snapshot().fault, Some(FeedFault::Malformed));
    assert!(!owner.snapshot().terminal_observed());
}
