// Standalone synthetic child. Never bundled into the development GUI.
import Foundation
import DevelopmentProcess
let mode = (try? String(contentsOfFile: "fixture-mode", encoding: .utf8)) ?? "normal"
var revision = 0
var on = false
func status() {
    revision += 1
    print("{\"protocolVersion\":1,\"type\":\"status\",\"sourceContinuousMs\":\(mode == "normal" || mode.hasPrefix("expiry") ? ResidentProcess.now() : ResidentProcess.now()-4000),\"snapshot\":{\"protocolVersion\":1,\"source\":\"resident-owner\",\"state\":\"source-observation\",\"revision\":\(revision),\"validForMs\":3000,\"standby\":\"off\",\"registration\":\"disconnected\",\"caller\":null,\"work\":\"idle\",\"cleanup\":\"confirmed\",\"canRequestOn\":true,\"canRequestOff\":false,\"transition\":null,\"standbyUntil\":null,\"standbyEndingSoon\":false,\"standbyExpired\":false}}")
    fflush(stdout)
}
status()
// A confirmed close the helper makes with no GUI request (the helper no longer
// ends itself on a timer; this keeps that path tested). "expiry" is exit 0;
// "expiry-nonzero" is a close followed by a failing exit.
if mode.hasPrefix("expiry") {
    print("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":\(ResidentProcess.now()),\"outcome\":\"confirmed\"}")
    fflush(stdout)
    exit(mode == "expiry" ? 0 : 1)
}
while let line = readLine() {
    if line == "close" {
        if mode == "missing" { exit(0) }
        if mode == "malformed" { print("{\"type\":\"closed\"}") }
        if mode == "unknown" {
            print("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":\(ResidentProcess.now()),\"outcome\":\"unknown\"}")
        }
        print("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":\(ResidentProcess.now()),\"outcome\":\"confirmed\"}")
        if mode == "trailing" { print("unexpected") }
        if mode == "partial" { print("x", terminator: "") }
        fflush(stdout)
        exit(mode == "nonzero" ? 3 : 0)
    }
    exit(3) // fixture has no command or live operation
}
exit(4)
