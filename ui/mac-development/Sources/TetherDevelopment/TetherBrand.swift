import AppKit
import CoreGraphics

// O2 CONNECTIVE のブランドのロゴ（gateway mark）。O2-CONNECTIVE-Derivation-Products の public/gateway-mark.svg から取り込み、
// 図形のデータ（path と塗り）は変えていない（Issue #392）。ui/tauri/src/Brand.tsx が正本で、下の範囲が
// ui/tauri の Brand.tsx・ui/tauri/design/index.html・もう一方の Mac app と同じであることは ui/tauri/design/check_tokens.py が検査する。
// brand:start
enum TetherBrandValue {
    /// gateway-mark.svg（viewBox 0 0 100 100）の path と塗り。
    static let paths: [(d: String, fill: Int)] = [
        ("M0 32c0-7 4-12 11-15L49 1c9-4 17 1 17 10v7c0 3-1 5-6 6l-5 3c-3 1-5 3-7 3-9 0-16 8-16 18v39l-20 9C5 99 0 94 0 87V32Z", 0x065292),
        ("M48 30c0-8 5-13 12-12 2 0 4 1 6 2l25 11c6 3 9 8 9 15v41c0 8-5 12-12 9l-24-9V48c0-10-7-18-16-18Z", 0x45B035),
    ]
    /// SVG の path データ（M L H V C Z の大小文字）を CGPath にする。
    static func cgPath(_ d: String) -> CGPath {
        struct Parser {
            let bytes: [UInt8]
            var index = 0
            var path = CGMutablePath()
            var x: CGFloat = 0
            var y: CGFloat = 0
            var startX: CGFloat = 0
            var startY: CGFloat = 0

            init(_ value: String) { bytes = Array(value.utf8) }

            mutating func skipSeparators() {
                while index < bytes.count && (bytes[index] == 32 || bytes[index] == 9 || bytes[index] == 10 || bytes[index] == 13 || bytes[index] == 44) { index += 1 }
            }

            mutating func number() -> CGFloat? {
                skipSeparators()
                let begin = index
                if index < bytes.count && (bytes[index] == 43 || bytes[index] == 45) { index += 1 }
                var digits = 0
                while index < bytes.count && bytes[index] >= 48 && bytes[index] <= 57 { index += 1; digits += 1 }
                if index < bytes.count && bytes[index] == 46 {
                    index += 1
                    while index < bytes.count && bytes[index] >= 48 && bytes[index] <= 57 { index += 1; digits += 1 }
                }
                guard digits > 0 else { index = begin; return nil }
                if index < bytes.count && (bytes[index] == 69 || bytes[index] == 101) {
                    let exponent = index
                    index += 1
                    if index < bytes.count && (bytes[index] == 43 || bytes[index] == 45) { index += 1 }
                    let exponentDigits = index
                    while index < bytes.count && bytes[index] >= 48 && bytes[index] <= 57 { index += 1 }
                    if exponentDigits == index { index = exponent }
                }
                guard let value = Double(String(decoding: bytes[begin..<index], as: UTF8.self)), value.isFinite else { return nil }
                return CGFloat(value)
            }

            mutating func parse() -> CGPath {
                var command: UInt8 = 0
                while true {
                    skipSeparators()
                    if index == bytes.count { return path }
                    let byte = bytes[index]
                    if (byte >= 65 && byte <= 90) || (byte >= 97 && byte <= 122) {
                        command = byte
                        index += 1
                        if command == 90 || command == 122 {
                            path.closeSubpath(); x = startX; y = startY; command = 0
                            continue
                        }
                        if command != 77 && command != 109 && command != 76 && command != 108 && command != 72 && command != 104 && command != 86 && command != 118 && command != 67 && command != 99 { return CGMutablePath() }
                    } else if command == 0 { return CGMutablePath() }

                    let relative = command >= 97
                    let kind = relative ? command - 32 : command
                    let oldX = x, oldY = y
                    guard let a = number() else { return CGMutablePath() }
                    switch kind {
                    case 77, 76:
                        guard let b = number() else { return CGMutablePath() }
                        let nx = relative ? oldX + a : a, ny = relative ? oldY + b : b
                        if kind == 77 { path.move(to: CGPoint(x: nx, y: ny)); startX = nx; startY = ny; command = relative ? 108 : 76 }
                        else { path.addLine(to: CGPoint(x: nx, y: ny)) }
                        x = nx; y = ny
                    case 72:
                        x = relative ? oldX + a : a; path.addLine(to: CGPoint(x: x, y: y))
                    case 86:
                        y = relative ? oldY + a : a; path.addLine(to: CGPoint(x: x, y: y))
                    case 67:
                        guard let b = number(), let c = number(), let e = number(), let f = number(), let g = number() else { return CGMutablePath() }
                        let c1 = CGPoint(x: relative ? oldX + a : a, y: relative ? oldY + b : b)
                        let c2 = CGPoint(x: relative ? oldX + c : c, y: relative ? oldY + e : e)
                        let end = CGPoint(x: relative ? oldX + f : f, y: relative ? oldY + g : g)
                        path.addCurve(to: end, control1: c1, control2: c2); x = end.x; y = end.y
                    default: return CGMutablePath()
                    }
                    skipSeparators()
                    if index == bytes.count { return path }
                    let next = bytes[index]
                    if (next >= 65 && next <= 90) || (next >= 97 && next <= 122) { continue }
                    if kind == 77 { command = relative ? 108 : 76 }
                }
            }
        }
        var parser = Parser(d)
        return parser.parse()
    }
}
// brand:end

enum TetherBrandImage {
    static func mark(size: CGFloat) -> NSImage {
        NSImage(size: NSSize(width: size, height: size), flipped: true) { rect in
            guard let context = NSGraphicsContext.current?.cgContext else { return false }
            context.saveGState()
            context.scaleBy(x: size / 100, y: size / 100)
            for item in TetherBrandValue.paths {
                let fill = item.fill
                context.setFillColor(CGColor(
                    srgbRed: CGFloat((fill >> 16) & 255) / 255,
                    green: CGFloat((fill >> 8) & 255) / 255,
                    blue: CGFloat(fill & 255) / 255,
                    alpha: 1
                ))
                context.addPath(TetherBrandValue.cgPath(item.d))
                context.fillPath()
            }
            context.restoreGState()
            return true
        }
    }
}
