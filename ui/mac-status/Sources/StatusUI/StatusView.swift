import SwiftUI
import AppKit
import StatusCore

public struct StatusView: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let snapshot: Snapshot
    let now: TimeInterval
    let fixture: Bool
    var renderStatic = false
    var textScale: CGFloat = 1
    public init(snapshot:Snapshot, now:TimeInterval, fixture:Bool, renderStatic:Bool=false, textScale:CGFloat=1) {
        self.snapshot=snapshot; self.now=now; self.fixture=fixture; self.renderStatic=renderStatic; self.textScale=textScale
    }
    private var fresh: Bool { snapshot.fresh(at:now) }
    private var frame: Frame? { snapshot.frame }
    private var unknown: Bool { snapshot.stickyUnknown || frame?.cleanup == "unknown" }
    private var feedback: SetupFeedback? { snapshot.setupFeedback(at:now) }
    private var title: String {
        if unknown { return "回収を確認できません" }
        if let feedback { return feedback.title }
        if snapshot.terminalObserved && (fresh || snapshot.sourceClosed) {
            let prefix = snapshot.sourceClosed ? "状態配信終了・" : ""
            switch frame?.cleanup {
            case "not_started": return prefix + "開始せず終了しました"
            case "confirmed": return prefix + "終了・回収確認済み"
            default: return prefix + "終了報告・回収は未確認"
            }
        }
        if !fresh { return frame == nil ? "状態情報を待っています" : "状態の更新が止まっています" }
        if (frame?.stopping ?? 0) > 0 || frame?.phase == "stopping" { return "停止・回収中" }
        if snapshot.remaining(at:now) == 0 { return "期限に到達・停止確認中" }
        switch frame?.phase {
        case "preparing": return "起動準備中"
        case "awaiting_approval": return "この Mac の承認待ち"
        case "connecting": return "接続を確認中"
        case "connected": return "接続中"
        default: return "状態を確認中"
        }
    }
    private var subtitle: String {
        if unknown { return "操作が終わっていても、安全に回収されたとは限りません。" }
        if let feedback { return feedback.detail + (snapshot.sourceClosed ? " 最終報告を表示しています。" : "") }
        if snapshot.terminalObserved && (fresh || snapshot.sourceClosed) && frame?.reason == "transport_lost" { return "通信が終了しました。接続先とネットワークの状態を確認してください。自動で再接続はしません。" }
        if snapshot.sourceClosed && snapshot.terminalObserved { return "最終報告を保持しています。現在の接続を示す表示ではありません。" }
        if !fresh { return "接続や停止を確認できません。表示から状態を推定しないでください。" }
        return "この画面は状態の確認専用です。新しい操作を許可しません。"
    }
    private var tone: Color { unknown || (frame?.phase == "failed" && feedback != nil) ? .primary : (fresh ? .accentColor : .secondary) }
    private var metricsCurrent: Bool { fresh && !unknown && frame?.terminal != true }
    private var remaining: String {
        guard metricsCurrent, let s = snapshot.remaining(at:now) else { return "—" }
        return String(format:"%02d:%02d",s/60,s%60)
    }
    private var content: some View {
            VStack(alignment:.leading,spacing:24) {
                HStack(alignment:.firstTextBaseline) {
                    Text("Tether").font(.system(size:20*textScale,weight:.semibold))
                    Spacer()
                    Text("状態確認専用").font(.system(size:12*textScale)).foregroundStyle(Color.primary.opacity(0.72))
                }
                if fixture { Label("表示テスト・実接続なし",systemImage:"testtube.2").font(.system(size:14*textScale,weight:.semibold)).foregroundStyle(Color.primary.opacity(0.8)) }
                VStack(alignment:.leading,spacing:10) {
                    HStack(alignment:.top,spacing:10) {
                        if feedback?.isProcessing == true {
                            if renderStatic || reduceMotion {
                                Image(systemName:"hourglass").accessibilityLabel("処理中")
                            } else {
                                ProgressView().controlSize(.small).accessibilityLabel("処理中")
                            }
                        } else {
                            Image(systemName:unknown ? "exclamationmark.triangle.fill" : (feedback?.symbol ?? (fresh ? "circle.inset.filled" : "questionmark.circle")))
                                .accessibilityHidden(true)
                        }
                        Text(title).fixedSize(horizontal:false,vertical:true)
                    }.font(.system(size:20*textScale,weight:.semibold)).foregroundStyle(tone)
                    Text(subtitle).foregroundStyle(Color.primary.opacity(0.72)).fixedSize(horizontal:false,vertical:true)
                }.accessibilityElement(children:.combine)
                ViewThatFits(in:.horizontal) {
                    HStack(spacing:12) { metric("承認の残り時間",remaining); metric("実行中の操作",metricsCurrent ? String(frame?.running ?? 0) : "—") }
                    VStack(spacing:12) { metric("承認の残り時間",remaining); metric("実行中の操作",metricsCurrent ? String(frame?.running ?? 0) : "—") }
                }
                Text("件数は管理中の操作数です。OS 全体のプロセス数ではありません。").font(.system(size:12*textScale)).foregroundStyle(Color.primary.opacity(0.72))
                VStack(alignment:.leading,spacing:8) {
                    Text("停止と回収").font(.system(size:14*textScale,weight:.semibold))
                    Text(cleanupText).fixedSize(horizontal:false,vertical:true)
                    if snapshot.terminalObserved { Text(endReason).font(.system(size:12*textScale)).fixedSize(horizontal:false,vertical:true) }
                    Text("残り時間は受信情報からの目安です。0 秒でも停止完了の証明にはなりません。")
                        .font(.system(size:12*textScale)).foregroundStyle(Color.primary.opacity(0.72)).fixedSize(horizontal:false,vertical:true)
                }
                Divider()
                VStack(alignment:.leading,spacing:8) {
                    Label("実行時の権限: 現在のユーザー",systemImage:"person.crop.circle.badge.exclamationmark").font(.system(size:14*textScale,weight:.semibold))
                    Text("サンドボックスではありません。このユーザーがアクセスできるファイルやネットワークに届きます。")
                    Text("画面を閉じてもエージェントは停止しません。実行済みの変更は元に戻りません。プロセス群から離れた子孫の停止は保証しません。")
                }.font(.system(size:12*textScale)).foregroundStyle(Color.primary.opacity(0.72)).fixedSize(horizontal:false,vertical:true)
            }.font(.system(size:14*textScale)).padding(28).frame(maxWidth:720,alignment:.leading).frame(maxWidth:.infinity)
    }
    public var body: some View {
        Group {
            if renderStatic { content } else { ScrollView { content } }
        }.background(Color(nsColor:.windowBackgroundColor)).frame(minWidth:320,minHeight:420)
        .accessibilityLabel("Tether この Mac の状態")
    }
    private var endReason: String {
        let names = ["none":"理由の報告なし", "revoked":"承認の失効", "deadline":"承認期限", "transport_lost":"通信断", "runtime_fault":"実行基盤の異常", "terminal_cleanup_unknown":"回収未確認", "canceled":"操作取消", "command_timeout":"操作の制限時間", "config_error":"設定の確認失敗", "key_error":"接続用の鍵の確認失敗", "preparation_error":"実行環境の準備失敗", "connection_error":"接続構成の失敗", "approval_not_completed":"承認未完了"]
        return "最終報告の理由: " + (names[frame?.reason ?? ""] ?? "不明")
    }
    private var cleanupText: String {
        if snapshot.stickyUnknown { return "回収未確認のまま終了しました。関連するプロセスが残っている可能性があります。" }
        if unknown { return "回収を確認できていません。完了として扱わないでください。" }
        if snapshot.terminalObserved && (fresh || snapshot.sourceClosed) && frame?.cleanup == "not_started" { return "最終報告: 実行は開始されていません。" }
        if snapshot.terminalObserved && (fresh || snapshot.sourceClosed) && frame?.cleanup == "pending" { return "最終報告: 回収は未確認です。確認が続いているとは限りません。" }
        if !fresh { return frame == nil ? "状態配信が接続されていません。" : "最終受信: \(frame?.cleanup == "confirmed" ? "回収確認済み" : "回収確認前")。現在の状態は不明です。" }
        switch frame?.cleanup {
        case "confirmed": return "所有するプロセスと入出力の回収を確認しました。"
        case "pending": return frame?.phase == "stopping" || (frame?.stopping ?? 0) > 0 ? "所有するプロセスと入出力の回収を待っています。" : "作業中です。終了時に回収結果を確認します。"
        case "not_started": return "まだ実行を開始していません。"
        default: return "回収状態を確認できません。"
        }
    }
    private func metric(_ label:String,_ value:String) -> some View {
        VStack(alignment:.leading,spacing:8) {
            Text(label).font(.system(size:12*textScale)).foregroundStyle(Color.primary.opacity(0.72))
            Text(value).font(.system(size:30*textScale,weight:.medium,design:.rounded)).monospacedDigit()
        }.frame(minWidth:140*textScale,maxWidth:.infinity,alignment:.leading).padding(16)
            .background(RoundedRectangle(cornerRadius:12).fill(Color.primary.opacity(0.045)))
            .accessibilityElement(children:.combine)
    }
}
