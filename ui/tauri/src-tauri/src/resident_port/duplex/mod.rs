//! Internal wire and concrete IO ownership; no production launch factory exists.
mod closure;
mod exchange;
#[cfg(target_os = "macos")]
pub(crate) mod owner;
#[cfg(target_os = "macos")]
mod pump;
mod startup;
mod tracking;
mod wire;

#[cfg(all(test, target_os = "macos"))]
mod fixture_python;
#[cfg(all(test, target_os = "macos"))]
mod owned_close_tests;
#[cfg(all(test, target_os = "macos"))]
mod pipe_tests;
#[cfg(test)]
mod tracking_tests;

#[cfg(test)]
mod closure_tests;
#[cfg(test)]
mod test_frames;
#[cfg(test)]
mod tests;
