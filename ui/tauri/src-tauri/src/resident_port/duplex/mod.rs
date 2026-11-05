//! Wire Close evidence only. The exact pipe/child/join owners remain unbound.
mod closure;
mod wire;

#[cfg(test)]
mod closure_tests;
#[cfg(test)]
mod test_frames;
#[cfg(test)]
mod tests;
