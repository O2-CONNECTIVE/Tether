use super::{FeedFault, Framing, BUFFER_BYTES, MAX_LINE_BYTES};

fn frame(seq: u64) -> Vec<u8> {
    format!(r#"{{"version":1,"source_continuous_ms":10000,"seq":{seq},"phase":"connected","reason":"none","grant_remaining_ms":120000,"starting_workers":0,"running_workers":1,"stopping_workers":0,"cleanup":"pending","terminal_cleanup_unknown":false,"terminal":false}}"#).into_bytes()
}
fn line(seq: u64) -> Vec<u8> {
    let mut bytes = frame(seq);
    bytes.push(b'\n');
    bytes
}

#[test]
fn every_chunk_boundary_preserves_one_complete_frame() {
    let bytes = line(1);
    for split in 0..=bytes.len() {
        let mut f = Framing::new(10.0);
        for chunk in [&bytes[..split], &bytes[split..]] {
            if !chunk.is_empty() { f.push_read(chunk, || 10.0); }
        }
        assert_eq!(f.snapshot().fault, None);
        assert_eq!(f.snapshot().frame.as_ref().expect("frame").seq, 1);
        f.eof();
        assert!(f.snapshot().source_closed());
        assert_eq!(f.snapshot().frame.as_ref().expect("retained").cleanup, "pending");
        assert!(!f.snapshot().fresh(10.0));
    }
}

#[test]
fn bounded_line_memory_and_partial_eof() {
    let mut f = Framing::new(10.0);
    f.push_read(&frame(1), || 10.0);
    assert_eq!(f.snapshot().fault, Some(FeedFault::Unavailable));
    f.eof();
    assert_eq!(f.snapshot().fault, Some(FeedFault::Malformed));
    let mut f = Framing::new(10.0);
    let mut exact = frame(1);
    exact.resize(MAX_LINE_BYTES, b' ');
    exact.push(b'\n');
    f.push_read(&exact, || 10.0);
    assert_eq!(f.snapshot().fault, None);
    let mut f = Framing::new(10.0);
    f.push_read(&[b' '; MAX_LINE_BYTES], || 10.0);
    f.push_read(&[b' '; 4096], || 10.0);
    assert_eq!(f.snapshot().fault, Some(FeedFault::Malformed));
    assert_eq!(f.buffer.len(), BUFFER_BYTES);
    assert_eq!(f.len, BUFFER_BYTES);
}

#[test]
fn rate_limit_is64_per_window_and_resets_at_one_second() {
    let mut rejected = Framing::new(10.0);
    let mut accepted = Framing::new(10.0);
    for seq in 1..=64 {
        rejected.push_read(&line(seq), || 10.0);
        accepted.push_read(&line(seq), || 10.0);
    }
    rejected.push_read(&line(65), || 10.999);
    assert_eq!(rejected.snapshot().fault, Some(FeedFault::Rate));
    assert_eq!(rejected.snapshot().frame.as_ref().expect("retained").seq, 64);
    accepted.push_read(&line(65), || 11.0);
    assert_eq!(accepted.snapshot().fault, None);
    assert_eq!(accepted.snapshot().frame.as_ref().expect("frame").seq, 65);
}

#[test]
fn each_complete_line_gets_its_own_clock_and_failure_stops_input() {
    let mut f = Framing::new(10.0);
    let mut bytes = line(1);
    bytes.extend(line(2));
    let mut ticks = [10.0, 9.0].into_iter();
    f.push_read(&bytes, || ticks.next().expect("one clock per line"));
    assert_eq!(f.snapshot().fault, Some(FeedFault::Clock));
    f.eof();
    f.push_read(&line(3), || 10.0);
    assert_eq!(f.snapshot().fault, Some(FeedFault::Clock));
}

#[test]
fn crlf_utf8_empty_line_and_caller_read_bounds_are_explicit() {
    let mut f = Framing::new(10.0);
    let mut crlf = frame(1);
    crlf.extend(b"\r\n");
    f.push_read(&crlf, || 10.0);
    assert_eq!(f.snapshot().fault, None);
    for invalid in [vec![0xff, b'\n'], vec![b'\n']] {
        let mut f = Framing::new(10.0);
        f.push_read(&invalid, || 10.0);
        assert_eq!(f.snapshot().fault, Some(FeedFault::Malformed));
    }
    for invalid in [Vec::new(), vec![b' '; 4097]] {
        let mut f = Framing::new(10.0);
        f.push_read(&invalid, || 10.0);
        assert_eq!(f.snapshot().fault, Some(FeedFault::Read));
    }
}
