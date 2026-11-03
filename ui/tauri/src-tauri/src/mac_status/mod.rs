// Pure Mac status input candidate, deliberately disconnected from native IO,
// Tauri commands and React. Swift StatusCore is the input reference.
mod decoder;
mod framing;
pub(crate) mod output;
mod presentation;
pub(crate) mod state;

#[cfg(test)]
mod golden_tests;
