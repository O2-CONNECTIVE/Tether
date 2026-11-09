use resident_preparation::NativeTerminal;
fn require_copy<T: Copy>() {}
fn main() {
    require_copy::<NativeTerminal>();
}
