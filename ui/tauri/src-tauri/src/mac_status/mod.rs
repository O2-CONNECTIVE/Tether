// Read-only Mac input prerequisites, disconnected from production startup,
// Tauri commands and React. Swift StatusCore is the input reference.
mod decoder;
mod framing;
pub(crate) mod output;
mod presentation;
pub(crate) mod state;

#[cfg(test)]
mod golden_tests;

mod continuous_clock;
#[cfg(target_os = "macos")]
mod native_input;
mod provider;
mod reader_owner;
