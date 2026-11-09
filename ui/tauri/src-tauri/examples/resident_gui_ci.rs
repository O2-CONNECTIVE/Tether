#[cfg(all(target_os = "macos", resident_gui_fixture))]
fn main() {
    rescue_host_shell::run_resident_gui_fixture();
}
#[cfg(not(all(target_os = "macos", resident_gui_fixture)))]
fn main() {
    // No ordinary build can activate the disposable CI composition.
    std::process::exit(2);
}
