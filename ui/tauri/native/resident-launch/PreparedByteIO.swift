import Foundation

enum PreparedByteOperation {
    case read(UnsafeMutablePointer<UInt8>?, UInt32)
    case write(UnsafePointer<UInt8>?, UInt32)
    case halfClose
}
struct PreparedByteResult {
    enum Tag: UInt32 { case rejected = 0, bytes = 1, pending = 2, eof = 3, halfClosed = 4, unknown = 5 }
    let tag: Tag
    var count: UInt32 = 0
    var packed: UInt64 { UInt64(tag.rawValue) << 32 | UInt64(count) }
}
