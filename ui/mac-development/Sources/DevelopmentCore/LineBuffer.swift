import Foundation

public struct LineBuffer {
    private var partial = Data()
    private var since: UInt64?
    public init() {}
    public var isEmpty: Bool { partial.isEmpty }
    public func checkDeadline(at now: UInt64) throws {
        if let since, now < since || now-since >= 1000 { throw Invalid.input }
    }
    public mutating func receive(_ bytes: [UInt8], at now: UInt64) throws -> [Data] {
        guard bytes.count <= 4096 else { throw Invalid.input }
        try checkDeadline(at: now)
        var frames: [Data] = []
        for byte in bytes {
            if byte == 10 {
                frames.append(partial); partial.removeAll(keepingCapacity: true); since = nil
            } else {
                guard partial.count < 4096 else { throw Invalid.input }
                if partial.isEmpty { since = now }; partial.append(byte)
            }
        }
        return frames
    }
}
