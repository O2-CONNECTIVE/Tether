import Foundation

public enum Invalid: Error { case input }
indirect enum Value {
    case object([String:Value]), text(String), number(UInt64), bool(Bool), null
}
// Finite JSON subset used by the resident protocol. Duplicate decoded keys,
// arrays, fractional/negative numbers, oversized and deeply nested input fail.
struct Parser {
    let bytes: [UInt8]
    var i = 0
    mutating func whitespace() { while i < bytes.count && [9, 13, 32].contains(bytes[i]) { i += 1 } }
    mutating func take(_ byte: UInt8) throws {
        whitespace()
        guard i < bytes.count, bytes[i] == byte else { throw Invalid.input }
        i += 1
    }
    mutating func string() throws -> String {
        try take(34)
        let start = i - 1
        var escaped = false
        while i < bytes.count {
            let b = bytes[i]; i += 1
            if b < 32 { throw Invalid.input }
            if !escaped && b == 34 {
                return try JSONDecoder().decode(String.self, from: Data(bytes[start..<i]))
            }
            if !escaped && b == 92 { escaped = true } else { escaped = false }
        }
        throw Invalid.input
    }
    mutating func literal(_ s: String) -> Bool {
        let b = Array(s.utf8)
        guard bytes.count - i >= b.count, Array(bytes[i..<(i+b.count)]) == b else { return false }
        i += b.count; return true
    }
    mutating func value(depth: Int = 0) throws -> Value {
        whitespace()
        guard i < bytes.count, depth <= 3 else { throw Invalid.input }
        if bytes[i] == 123 {
            i += 1; var result: [String:Value] = [:]
            while true {
                let key = try string()
                guard result[key] == nil, result.count < 16 else { throw Invalid.input }
                try take(58); result[key] = try value(depth: depth+1); whitespace()
                guard i < bytes.count else { throw Invalid.input }
                if bytes[i] == 125 { i += 1; return .object(result) }
                try take(44)
            }
        }
        if bytes[i] == 34 { return .text(try string()) }
        if literal("null") { return .null }
        if literal("true") { return .bool(true) }
        if literal("false") { return .bool(false) }
        let start = i
        while i < bytes.count && (48...57).contains(bytes[i]) { i += 1 }
        guard i > start, i-start == 1 || bytes[start] != 48,
              let number = UInt64(String(decoding: bytes[start..<i], as: UTF8.self)),
              number <= 9_007_199_254_740_991 else { throw Invalid.input }
        return .number(number)
    }
}
struct Object {
    let fields: [String:Value]
    init(_ value: Value, _ keys: [String]) throws {
        guard case let .object(fields) = value, Set(fields.keys) == Set(keys) else { throw Invalid.input }
        self.fields = fields
    }
    func number(_ key: String) throws -> UInt64 {
        guard case let .number(n)? = fields[key] else { throw Invalid.input }; return n
    }
    func text(_ key: String, _ allowed: [String]) throws -> String {
        guard case let .text(s)? = fields[key], allowed.contains(s) else { throw Invalid.input }; return s
    }
    func bool(_ key: String) throws -> Bool {
        guard case let .bool(b)? = fields[key] else { throw Invalid.input }; return b
    }
}
