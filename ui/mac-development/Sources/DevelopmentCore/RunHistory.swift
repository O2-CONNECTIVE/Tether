import Foundation

public struct RunRecord: Equatable {
    public let at, dir, command: String
    public static let unreadable = "（読めない記録）"
    public init(at: String, dir: String, command: String) {
        self.at = at; self.dir = dir; self.command = command
    }
}

public enum RunHistory {
    public static func recent(_ data: Data, limit: Int) -> [RunRecord] {
        guard limit > 0 else { return [] }
        return data.split(separator: 10, omittingEmptySubsequences: true)
            .suffix(limit).reversed().map(record)
    }

    // What the screen shows. Every cut is stated, never silent.
    public static func text(_ data: Data, limit: Int) -> String {
        let all = recent(data, limit: Int.max)
        guard !all.isEmpty else { return "実行したコマンドはありません" }
        let shown = all.prefix(limit).map { r -> String in
            let cut = r.command.count > 300 ? "\n…（全\(r.command.count)文字。以下省略）" : ""
            return "\(r.at)  \(r.dir)\n\(r.command.prefix(300))\(cut)"
        }
        return (["全\(all.count)件のうち直近\(shown.count)件"] + shown).joined(separator: "\n\n")
    }

    private static func record(_ line: Data.SubSequence) -> RunRecord {
        do {
            var parser = Parser(bytes: Array(line))
            let value = try parser.value(); parser.whitespace()
            guard parser.i == line.count else { throw Invalid.input }
            let object = try Object(value, ["at", "dir", "command"])
            guard case let .text(at)? = object.fields["at"],
                  case let .text(dir)? = object.fields["dir"],
                  case let .text(command)? = object.fields["command"] else { throw Invalid.input }
            return RunRecord(at: at, dir: dir, command: command)
        } catch {
            return RunRecord(at: "", dir: "", command: RunRecord.unreadable)
        }
    }
}
