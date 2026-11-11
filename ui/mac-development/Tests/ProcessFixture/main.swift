// Standalone synthetic child. Never bundled into the development GUI.
import Foundation
import DevelopmentProcess
var revision = 0
var on = false
func status() {
    revision += 1
    print("{\"protocolVersion\":1,\"type\":\"status\",\"sourceContinuousMs\":\(ResidentProcess.now()),\"snapshot\":{\"protocolVersion\":1,\"source\":\"resident-owner\",\"state\":\"source-observation\",\"revision\":\(revision),\"validForMs\":3000,\"standby\":\"off\",\"registration\":\"disconnected\",\"caller\":null,\"work\":\"idle\",\"cleanup\":\"confirmed\",\"canRequestOn\":true,\"canRequestOff\":false,\"transition\":null}}")
    fflush(stdout)
}
status()
while let line = readLine() {
    if line == "close" {
        print("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":\(ResidentProcess.now()),\"outcome\":\"confirmed\"}")
        fflush(stdout)
        exit(0)
    }
    exit(3) // fixture has no command or live operation
}
exit(4)
