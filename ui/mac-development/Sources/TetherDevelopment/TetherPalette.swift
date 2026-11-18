import AppKit

// O2 CONNECTIVE Derivation-Products の「C2 friendly enterprise theme」から写した色（Issue #392）。
// 値の正本は ui/tauri/design/tokens.css の ref。ui/mac-status の同名ファイルと下の範囲が同じであること、
// tokens.css と同じ値であることは ui/tauri/design/check_tokens.py が検査する。ダークは OS 標準の色のまま。
// palette:start
enum TetherPaletteValue {
    static let canvas = 0xF8FBFA
    static let panel = 0xFFFFFF
    static let ink = 0x33413D
    static let muted = 0x56645F
    static let line = 0xDFE9E5
    static let info = 0x065292
    static let warning = 0x805000
}
// palette:end

/// ライトは O2 の値、ダークは今までの OS 標準の色。window の外観が変われば自動で切り替わる。
enum TetherPalette {
    private static func color(_ light: Int, dark: NSColor) -> NSColor {
        NSColor(name: nil) { appearance in
            guard appearance.bestMatch(from: [.darkAqua, .aqua]) == .aqua else { return dark }
            return NSColor(srgbRed: CGFloat(light >> 16 & 0xFF) / 255, green: CGFloat(light >> 8 & 0xFF) / 255, blue: CGFloat(light & 0xFF) / 255, alpha: 1)
        }
    }
    static let canvas = color(TetherPaletteValue.canvas, dark: .windowBackgroundColor)
    static let ink = color(TetherPaletteValue.ink, dark: .labelColor)
    static let muted = color(TetherPaletteValue.muted, dark: .secondaryLabelColor)
    /// 注意の文字色。ライトでは AA（4.5:1）を満たす暗い琥珀、ダークは今までの systemOrange。
    static let warning = color(TetherPaletteValue.warning, dark: .systemOrange)
    /// OS 標準の書体（SF Pro とヒラギノ角ゴ）。書体だけは O2 に合わせない（Issue #397）。
    /// 太字は tokens.css の見出しと mac-status に揃えて semibold（600）。
    static func font(_ size: CGFloat, bold: Bool = false) -> NSFont {
        .systemFont(ofSize: size, weight: bold ? .semibold : .regular)
    }
}
