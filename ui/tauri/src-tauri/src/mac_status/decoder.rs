//! Strict, bounded parity with StatusCore.Decoder; no general JSON normalization.
pub(crate) const MAX_LINE_BYTES: usize = 2048;
const SAFE_INTEGER: u64 = 9_007_199_254_740_991;
const KEYS: [&str; 12] = [
    "version", "source_continuous_ms", "seq", "phase", "reason",
    "grant_remaining_ms", "starting_workers", "running_workers",
    "stopping_workers", "cleanup", "terminal_cleanup_unknown", "terminal",
];

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct Frame {
    pub(crate) source_ms: u64,
    pub(crate) seq: u64,
    pub(crate) phase: String,
    pub(crate) reason: String,
    pub(crate) remaining: Option<u32>,
    pub(crate) starting: u8,
    pub(crate) running: u8,
    pub(crate) stopping: u8,
    pub(crate) cleanup: String,
    pub(crate) terminal_unknown: bool,
    pub(crate) terminal: bool,
}

/// Deliberately carries no raw input or diagnostic payload.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Invalid;

#[derive(Clone, Copy)]
enum Atom<'a> {
    Text(&'a str),
    Integer(u64),
    Boolean(bool),
    Null,
}

struct FlatJson<'a> {
    bytes: &'a [u8],
    index: usize,
}

impl<'a> FlatJson<'a> {
    fn white(&mut self) {
        while matches!(self.bytes.get(self.index), Some(b'\t' | b'\n' | b'\r' | b' ')) {
            self.index += 1;
        }
    }

    fn byte(&mut self, expected: u8) -> Result<(), Invalid> {
        self.white();
        if self.bytes.get(self.index) != Some(&expected) {
            return Err(Invalid);
        }
        self.index += 1;
        Ok(())
    }

    fn string(&mut self) -> Result<&'a str, Invalid> {
        self.byte(b'"')?;
        let start = self.index;
        while let Some(&byte) = self.bytes.get(self.index) {
            if byte == b'"' {
                let text = std::str::from_utf8(&self.bytes[start..self.index])
                    .map_err(|_| Invalid)?;
                self.index += 1;
                return Ok(text);
            }
            if !(32..127).contains(&byte) || byte == b'\\' {
                return Err(Invalid);
            }
            self.index += 1;
        }
        Err(Invalid)
    }

    fn literal(&mut self, literal: &[u8]) -> bool {
        if self.bytes[self.index..].starts_with(literal) {
            self.index += literal.len();
            true
        } else {
            false
        }
    }

    fn atom(&mut self) -> Result<Atom<'a>, Invalid> {
        self.white();
        if self.bytes.get(self.index) == Some(&b'"') {
            return self.string().map(Atom::Text);
        }
        if self.literal(b"null") { return Ok(Atom::Null); }
        if self.literal(b"true") { return Ok(Atom::Boolean(true)); }
        if self.literal(b"false") { return Ok(Atom::Boolean(false)); }
        let start = self.index;
        let mut value = 0u64;
        while let Some(&byte) = self.bytes.get(self.index) {
            if !byte.is_ascii_digit() { break; }
            value = value.checked_mul(10)
                .and_then(|n| n.checked_add(u64::from(byte - b'0')))
                .ok_or(Invalid)?;
            self.index += 1;
        }
        if self.index == start || (self.index - start > 1 && self.bytes[start] == b'0') {
            return Err(Invalid);
        }
        Ok(Atom::Integer(value))
    }

    fn object(&mut self) -> Result<[Option<Atom<'a>>; 12], Invalid> {
        let mut values = [None; 12];
        self.byte(b'{')?;
        loop {
            let key = self.string()?;
            let slot = KEYS.iter().position(|candidate| *candidate == key).ok_or(Invalid)?;
            // Check before assignment: repeated keys can never erase an earlier value.
            if values[slot].is_some() { return Err(Invalid); }
            self.byte(b':')?;
            values[slot] = Some(self.atom()?);
            self.white();
            if self.bytes.get(self.index) == Some(&b'}') {
                self.index += 1;
                break;
            }
            self.byte(b',')?;
        }
        self.white();
        if self.index != self.bytes.len() || values.iter().any(Option::is_none) {
            return Err(Invalid);
        }
        Ok(values)
    }
}

fn number(atom: Option<Atom<'_>>, max: u64) -> Result<u64, Invalid> {
    match atom {
        Some(Atom::Integer(n)) if n <= max => Ok(n),
        _ => Err(Invalid),
    }
}

fn text<'a>(atom: Option<Atom<'a>>, allowed: &[&str]) -> Result<&'a str, Invalid> {
    match atom {
        Some(Atom::Text(s)) if allowed.contains(&s) => Ok(s),
        _ => Err(Invalid),
    }
}

fn boolean(atom: Option<Atom<'_>>) -> Result<bool, Invalid> {
    match atom {
        Some(Atom::Boolean(value)) => Ok(value),
        _ => Err(Invalid),
    }
}

pub(crate) fn decode(data: &[u8]) -> Result<Frame, Invalid> {
    if data.is_empty() || data.len() > MAX_LINE_BYTES { return Err(Invalid); }
    let [version, source, sequence, phase, reason, remaining, starting, running,
        stopping, cleanup, terminal_unknown, terminal] =
        FlatJson { bytes: data, index: 0 }.object()?;
    if number(version, 1)? != 1 { return Err(Invalid); }
    let source_ms = number(source, SAFE_INTEGER)?;
    let seq = number(sequence, SAFE_INTEGER)?;
    if seq == 0 { return Err(Invalid); }
    let phase = text(phase, &[
        "checking_config", "loading_key", "preparing", "awaiting_approval",
        "connecting", "connected", "stopping", "closed", "failed",
    ])?;
    let reason = text(reason, &[
        "none", "revoked", "deadline", "transport_lost", "runtime_fault",
        "terminal_cleanup_unknown", "canceled", "command_timeout", "config_error",
        "key_error", "preparation_error", "connection_error", "approval_not_completed",
    ])?;
    let remaining = match remaining {
        Some(Atom::Null) => None,
        value => Some(number(value, 86_400_000)? as u32),
    };
    let starting = number(starting, 1)? as u8;
    let running = number(running, 1)? as u8;
    let stopping = number(stopping, 1)? as u8;
    let cleanup = text(cleanup, &["not_started", "pending", "confirmed", "unknown"])?;
    let terminal_unknown = boolean(terminal_unknown)?;
    let terminal = boolean(terminal)?;
    let workers = starting + running + stopping;
    if workers > 1 || terminal != matches!(phase, "closed" | "failed") {
        return Err(Invalid);
    }
    if terminal_unknown
        && !(terminal && phase == "failed" && cleanup == "unknown"
            && reason == "terminal_cleanup_unknown")
    {
        return Err(Invalid);
    }
    if reason == "terminal_cleanup_unknown" && !terminal_unknown { return Err(Invalid); }
    if matches!(reason, "config_error" | "key_error" | "preparation_error"
        | "connection_error" | "approval_not_completed")
        && !(terminal && phase == "failed" && !terminal_unknown
            && matches!(cleanup, "not_started" | "confirmed")
            && workers == 0 && remaining.unwrap_or(0) == 0)
    {
        return Err(Invalid);
    }
    if terminal && cleanup == "confirmed" && (workers != 0 || remaining.unwrap_or(0) != 0) {
        return Err(Invalid);
    }
    Ok(Frame {
        source_ms, seq, phase: phase.into(), reason: reason.into(), remaining,
        starting, running, stopping, cleanup: cleanup.into(), terminal_unknown, terminal,
    })
}

#[cfg(test)]
#[path = "decoder_tests.rs"]
mod tests;
