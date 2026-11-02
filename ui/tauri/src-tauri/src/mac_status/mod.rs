// Pure Mac status input candidate, deliberately disconnected from native IO,
// Tauri commands and presentation output. Swift StatusCore is the reference.
mod decoder;
mod framing;
mod state;

#[cfg(test)]
mod golden_tests;
