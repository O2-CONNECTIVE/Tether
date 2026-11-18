import SwiftUI
import AppKit

// O2 CONNECTIVE Derivation-Products の「C2 friendly enterprise theme」から写した色（Issue #392）。
// 値の正本は ui/tauri/design/tokens.css の ref。ui/mac-development の同名ファイルと下の範囲が同じであること、
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

/// ライトは O2 の値、ダークは今までの OS 標準の見た目。SwiftUI の colorScheme で切り替える。
public struct TetherPalette {
    let dark: Bool
    public init(_ scheme: ColorScheme) { dark = scheme == .dark }
    private func rgb(_ value: Int) -> Color {
        Color(.sRGB, red: Double(value >> 16 & 0xFF) / 255, green: Double(value >> 8 & 0xFF) / 255, blue: Double(value & 0xFF) / 255)
    }
    public var canvas: Color { dark ? Color(nsColor: .windowBackgroundColor) : rgb(TetherPaletteValue.canvas) }
    var panel: Color { dark ? Color.primary.opacity(0.045) : rgb(TetherPaletteValue.panel) }
    var line: Color { dark ? .clear : rgb(TetherPaletteValue.line) }
    var ink: Color { dark ? .primary : rgb(TetherPaletteValue.ink) }
    var muted: Color { dark ? Color.primary.opacity(0.72) : rgb(TetherPaletteValue.muted) }
    /// 「接続中」の文字色。primary の緑は操作の色なので使わない。
    var info: Color { dark ? .accentColor : rgb(TetherPaletteValue.info) }
}

extension Font {
    /// OS 標準の書体（SF Pro とヒラギノ角ゴ）。書体だけは O2 に合わせない（Issue #397）。
    static func tether(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight)
    }
}
